import base64
import contextlib
import csv
import importlib.util
import json
import io
import os
import tempfile
import unittest
from unittest import mock
from pathlib import Path


SCRIPT = Path(__file__).with_name("browser_capture_store.py")
SPEC = importlib.util.spec_from_file_location("browser_capture_store", SCRIPT)
STORE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(STORE)

PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j9WQAAAAASUVORK5CYII=")


def record(index=0, **changes):
    value = {
        "record_id": f"record_{index}",
        "attachment_id": f"attachment_{index}",
        "ordinal_in_message": index,
        "message_text_exact": f"Caption exact {index}\nsecond line",
        "posted_at_utc": "2025-01-02T03:04:05Z",
        "edited_at_utc": "",
        "author_display": "Example author",
        "source_message_permalink": f"https://user:secret@example.test/channel/message?token=hidden#{index}",
        "channel_id": "channel_1",
        "attachment_original_filename": f"original_{index}.png",
    }
    value.update(changes)
    return value


class BrowserCaptureStoreTests(unittest.TestCase):
    def test_cli_rebuild_does_not_report_corrupt_dataset_as_success(self):
        STORE.add_capture(self.root, self.source, record(42))
        (self.root / "images/attachment_42.png").write_bytes(b"changed")
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            status = STORE.main(["rebuild", "--root", str(self.root)])
        self.assertEqual(status, 1)
        self.assertEqual(json.loads(output.getvalue())["invalid_sidecars"], 1)
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name) / "store"
        self.source = Path(self.temp.name) / "source.png"
        self.source.write_bytes(PNG)

    def tearDown(self):
        self.temp.cleanup()

    def test_ten_png_records_rebuild_exact_counts_and_safe_metadata(self):
        for index in range(10):
            result = STORE.add_capture(self.root, self.source, record(index))
            self.assertEqual(result["status"], "added")
            self.assertEqual(result["record_count"], index + 1)
            saved = self.root / result["local_relative_path"]
            self.assertEqual(saved.read_bytes(), PNG)
            self.assertEqual(result["sha256"], STORE._hash_bytes(PNG))

        summary = STORE.rebuild_store(self.root)
        self.assertEqual(summary["record_count"], 10)
        self.assertFalse(summary["coverage_complete"])
        with (self.root / "manifest.csv").open(encoding="utf-8", newline="") as stream:
            manifest = list(csv.DictReader(stream))
        self.assertEqual(len(manifest), 10)
        self.assertEqual({row["attachment_id"] for row in manifest}, {f"attachment_{i}" for i in range(10)})
        self.assertTrue(all(row["file_size_bytes"] == str(len(PNG)) for row in manifest))

        quality = json.loads((self.root / "quality_audit.json").read_text(encoding="utf-8"))
        checkpoint = json.loads((self.root / "checkpoint.json").read_text(encoding="utf-8"))
        self.assertEqual(quality["valid_records"], 10)
        self.assertEqual(quality["invalid_sidecars"], 0)
        self.assertFalse(checkpoint["coverage_complete"])
        self.assertEqual(len(checkpoint["completed_attachment_ids"]), 10)
        self.assertEqual(checkpoint["unique_attachment_count"], 10)
        self.assertEqual(len(checkpoint["completed_records"]), 10)
        self.assertEqual(checkpoint["manifest_sha256"], STORE._hash_bytes((self.root / "manifest.csv").read_bytes()))
        self.assertEqual(checkpoint["audit_sha256"], STORE._hash_bytes((self.root / "quality_audit.json").read_bytes()))

        sidecar = json.loads((self.root / "metadata/record_0_attachment_0.json").read_text(encoding="utf-8"))
        self.assertFalse(sidecar["source_original_verified"])
        self.assertNotIn("secret", json.dumps(sidecar))
        self.assertNotIn("token", json.dumps(sidecar))
        self.assertEqual(sidecar["source_message_permalink"], "https://example.test/channel/message")

    def test_duplicate_retry_repairs_snapshot_after_json_commit(self):
        original = STORE._rebuild_store_locked
        with mock.patch.object(STORE, "_rebuild_store_locked", side_effect=STORE.StoreError("snapshot failed")):
            with self.assertRaises(STORE.StoreError):
                STORE.add_capture(self.root, self.source, record(20))
        self.assertTrue((self.root / "metadata/record_20_attachment_20.json").is_file())
        self.assertFalse((self.root / "manifest.csv").exists())
        with mock.patch.object(STORE, "_rebuild_store_locked", original):
            result = STORE.add_capture(self.root, self.source, record(20))
        self.assertEqual(result["status"], "duplicate")
        self.assertEqual(result["record_count"], 1)
        with (self.root / "manifest.csv").open(encoding="utf-8", newline="") as stream:
            self.assertEqual(len(list(csv.DictReader(stream))), 1)

    def test_caption_crlf_is_compared_byte_exactly(self):
        item = record(21, message_text_exact="line one\r\nline two")
        STORE.add_capture(self.root, self.source, item)
        STORE.rebuild_store(self.root)
        self.assertEqual((self.root / "metadata/record_21_attachment_21.txt").read_bytes(), b"line one\r\nline two")
        quality = json.loads((self.root / "quality_audit.json").read_text(encoding="utf-8"))
        self.assertEqual(quality["valid_records"], 1)
        self.assertEqual(quality["invalid_sidecars"], 0)

    def test_csv_formula_is_escaped_but_exact_caption_is_retained(self):
        item = record(22, message_text_exact="=1+1")
        STORE.add_capture(self.root, self.source, item)
        txt = self.root / "metadata/record_22_attachment_22.txt"
        sidecar = self.root / "metadata/record_22_attachment_22.json"
        self.assertEqual(txt.read_bytes(), b"=1+1")
        self.assertEqual(json.loads(sidecar.read_text(encoding="utf-8"))["message_text_exact"], "=1+1")
        with (self.root / "manifest.csv").open(encoding="utf-8", newline="") as stream:
            rows = list(csv.DictReader(stream))
        self.assertEqual(rows[0]["message_text_exact"], "'=1+1")

    def test_checkpoint_counts_records_and_unique_shared_attachments(self):
        STORE.add_capture(self.root, self.source, record(25, attachment_id="shared"))
        result = STORE.add_capture(self.root, self.source, record(26, attachment_id="shared"))
        self.assertEqual(result["record_count"], 2)
        checkpoint = json.loads((self.root / "checkpoint.json").read_text(encoding="utf-8"))
        self.assertEqual(checkpoint["unique_attachment_count"], 1)
        self.assertEqual(checkpoint["completed_attachment_ids"], ["shared"])
        self.assertEqual(checkpoint["completed_records"], [
            {"record_id": "record_25", "attachment_id": "shared"},
            {"record_id": "record_26", "attachment_id": "shared"},
        ])

    def test_snapshot_failure_is_detected_and_duplicate_retry_repairs_hashes(self):
        STORE.add_capture(self.root, self.source, record(23))
        real = STORE._atomic_replace_bytes

        def fail_audit(path, data):
            if path.name == "quality_audit.json":
                raise STORE.StoreError("injected snapshot failure")
            return real(path, data)

        with mock.patch.object(STORE, "_atomic_replace_bytes", side_effect=fail_audit):
            with self.assertRaises(STORE.StoreError):
                STORE.add_capture(self.root, self.source, record(24))
        with mock.patch.object(STORE, "_atomic_replace_bytes", real):
            STORE.add_capture(self.root, self.source, record(24))
        checkpoint = json.loads((self.root / "checkpoint.json").read_text(encoding="utf-8"))
        self.assertEqual(checkpoint["manifest_sha256"], STORE._hash_bytes((self.root / "manifest.csv").read_bytes()))
        self.assertEqual(checkpoint["audit_sha256"], STORE._hash_bytes((self.root / "quality_audit.json").read_bytes()))
        self.assertEqual(checkpoint["record_count"], 2)

    def test_retry_is_idempotent_and_orphan_files_resume_before_commit(self):
        item = record(1)
        first = STORE.add_capture(self.root, self.source, item)
        image_path = self.root / first["local_relative_path"]
        sidecar_path = self.root / "metadata/record_1_attachment_1.json"
        txt_path = sidecar_path.with_suffix(".txt")
        before_image = image_path.stat().st_mtime_ns
        before_txt = txt_path.stat().st_mtime_ns
        before_json = sidecar_path.stat().st_mtime_ns
        self.assertEqual(STORE.add_capture(self.root, self.source, item)["status"], "duplicate")
        self.assertEqual((image_path.stat().st_mtime_ns, txt_path.stat().st_mtime_ns, sidecar_path.stat().st_mtime_ns), (before_image, before_txt, before_json))

        orphan = record(2)
        orphan_image = self.root / "images/attachment_2.png"
        orphan_txt = self.root / "metadata/record_2_attachment_2.txt"
        orphan_image.parent.mkdir(parents=True, exist_ok=True)
        orphan_txt.parent.mkdir(parents=True, exist_ok=True)
        orphan_image.write_bytes(PNG)
        orphan_txt.write_bytes(orphan["message_text_exact"].encode("utf-8"))
        self.assertFalse((self.root / "metadata/record_2_attachment_2.json").exists())
        resumed = STORE.add_capture(self.root, self.source, orphan)
        self.assertEqual(resumed["status"], "added")
        self.assertTrue((self.root / "metadata/record_2_attachment_2.json").is_file())
        self.assertEqual(orphan_image.read_bytes(), PNG)

    def test_rebuild_excludes_corrupt_or_missing_artifacts_without_rewriting_them(self):
        result = STORE.add_capture(self.root, self.source, record(3))
        artifact = self.root / result["local_relative_path"]
        artifact.write_bytes(b"X" * len(PNG))
        sidecar_before = (self.root / "metadata/record_3_attachment_3.json").read_bytes()
        summary = STORE.rebuild_store(self.root)
        self.assertEqual(summary["record_count"], 0)
        self.assertEqual(artifact.read_bytes(), b"X" * len(PNG))
        self.assertEqual((self.root / "metadata/record_3_attachment_3.json").read_bytes(), sidecar_before)
        quality = json.loads((self.root / "quality_audit.json").read_text(encoding="utf-8"))
        self.assertEqual(quality["invalid_sidecars"], 1)
        self.assertEqual(quality["errors"][0]["error"], "artifact_hash_mismatch")

        artifact.unlink()
        STORE.rebuild_store(self.root)
        quality = json.loads((self.root / "quality_audit.json").read_text(encoding="utf-8"))
        self.assertEqual(quality["errors"][0]["error"], "artifact_missing_or_changed")

    def test_conflicting_bytes_or_caption_preserve_all_existing_files(self):
        item = record(4)
        result = STORE.add_capture(self.root, self.source, item)
        image_path = self.root / result["local_relative_path"]
        sidecar_path = self.root / "metadata/record_4_attachment_4.json"
        original_image, original_json = image_path.read_bytes(), sidecar_path.read_bytes()

        changed_caption = record(4, message_text_exact="different caption")
        with self.assertRaisesRegex(STORE.StoreError, "conflicts"):
            STORE.add_capture(self.root, self.source, changed_caption)

        self.source.write_bytes(PNG + b"changed")
        with self.assertRaisesRegex(STORE.StoreError, "conflicts"):
            STORE.add_capture(self.root, self.source, item)
        self.assertEqual(image_path.read_bytes(), original_image)
        self.assertEqual(sidecar_path.read_bytes(), original_json)

    def test_rejects_traversal_unsupported_fields_symlinks_and_oversize(self):
        with self.assertRaises(STORE.StoreError):
            STORE.add_capture(self.root, self.source, record(0, record_id="../escape"))
        with self.assertRaises(STORE.StoreError):
            STORE.add_capture(self.root, self.source, {**record(1), "extra_token": "secret"})

        link = Path(self.temp.name) / "source-link.png"
        try:
            link.symlink_to(self.source)
        except (OSError, NotImplementedError):
            link = None
        if link is not None:
            with self.assertRaises(STORE.StoreError):
                STORE.add_capture(self.root, link, record(2))

        oversized = Path(self.temp.name) / "large.bin"
        with oversized.open("wb") as stream:
            stream.truncate(STORE.MAX_SOURCE_BYTES + 1)
        with self.assertRaisesRegex(STORE.StoreError, "64 MiB"):
            STORE.add_capture(self.root, oversized, record(3))

    def test_rebuild_rejects_oversized_sidecar_and_symlink_directory(self):
        STORE.add_capture(self.root, self.source, record(30))
        sidecar = self.root / "metadata/record_30_attachment_30.json"
        sidecar.write_bytes(b" " * (STORE.MAX_RECORD_BYTES + 1))
        STORE.rebuild_store(self.root)
        quality = json.loads((self.root / "quality_audit.json").read_text(encoding="utf-8"))
        self.assertEqual(quality["valid_records"], 0)
        self.assertEqual(quality["errors"][0]["error"], "invalid_sidecar")

        outside = Path(self.temp.name) / "outside"
        outside.mkdir()
        import shutil
        shutil.rmtree(self.root / "images")
        try:
            (self.root / "images").symlink_to(outside, target_is_directory=True)
        except (OSError, NotImplementedError):
            self.skipTest("directory symlinks are unavailable")
        with self.assertRaises(STORE.StoreError):
            STORE.rebuild_store(self.root)

    def test_record_file_is_bounded_and_rejects_symlinks(self):
        path = Path(self.temp.name) / "record.json"
        path.write_text(json.dumps(record(31)), encoding="utf-8")
        self.assertEqual(STORE._read_record_file(path)["record_id"], "record_31")
        path.write_bytes(b" " * (STORE.MAX_RECORD_BYTES + 1))
        with self.assertRaisesRegex(STORE.StoreError, "1 MiB"):
            STORE._read_record_file(path)


if __name__ == "__main__":
    unittest.main()
