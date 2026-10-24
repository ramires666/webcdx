#!/usr/bin/env python3
"""Store already-permitted browser download captures without contacting services."""

from __future__ import annotations

import argparse
import contextlib
import csv
import hashlib
import json
import os
import re
import stat
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

MAX_SOURCE_BYTES = 64 * 1024 * 1024
MAX_RECORD_BYTES = 1024 * 1024
SCHEMA_VERSION = 1
ID_RE = re.compile(r"^[A-Za-z0-9_-]{1,128}$")
EXT_RE = re.compile(r"^\.[A-Za-z0-9]{1,10}$")
RECORD_FIELDS = (
    "record_id",
    "attachment_id",
    "ordinal_in_message",
    "message_text_exact",
    "posted_at_utc",
    "edited_at_utc",
    "author_display",
    "source_message_permalink",
    "channel_id",
    "attachment_original_filename",
)
MANIFEST_FIELDS = RECORD_FIELDS + ("local_relative_path", "sha256", "file_size_bytes")


class StoreError(Exception):
    """A fixed, path-free CLI error."""


def _safe_permalink(value: str) -> str:
    if not value:
        return ""
    try:
        parsed = urlsplit(value)
        if parsed.scheme not in ("http", "https") or not parsed.netloc:
            return ""
        host = parsed.hostname or ""
        if ":" in host and not host.startswith("["):
            host = f"[{host}]"
        if parsed.port:
            host += f":{parsed.port}"
        return urlunsplit((parsed.scheme.lower(), host, parsed.path, "", ""))
    except (ValueError, UnicodeError):
        return ""


def _validate_record(raw: object) -> dict:
    if not isinstance(raw, dict) or set(raw) - set(RECORD_FIELDS):
        raise StoreError("record contains unsupported fields")
    record = {}
    for field in RECORD_FIELDS:
        value = raw.get(field, "" if field != "ordinal_in_message" else 0)
        if field == "ordinal_in_message":
            if isinstance(value, bool) or not isinstance(value, int) or value < 0:
                raise StoreError("record ordinal is invalid")
        elif not isinstance(value, str):
            raise StoreError("record fields must be strings")
        record[field] = value
    for field in ("record_id", "attachment_id"):
        if not ID_RE.fullmatch(record[field]):
            raise StoreError("record identifiers are invalid")
    record["source_message_permalink"] = _safe_permalink(record["source_message_permalink"])
    if len(_json_bytes(record)) > MAX_RECORD_BYTES:
        raise StoreError("record exceeds the 1 MiB limit")
    return record


def _hash_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _read_source(path: Path) -> bytes:
    try:
        info = path.lstat()
    except OSError as exc:
        raise StoreError("source file is unavailable") from exc
    if path.is_symlink() or not stat.S_ISREG(info.st_mode):
        raise StoreError("source must be a regular nonsymlink file")
    if info.st_size > MAX_SOURCE_BYTES:
        raise StoreError("source exceeds the 64 MiB limit")
    try:
        with path.open("rb") as stream:
            data = stream.read(MAX_SOURCE_BYTES + 1)
    except OSError as exc:
        raise StoreError("source file could not be read") from exc
    if len(data) > MAX_SOURCE_BYTES:
        raise StoreError("source exceeds the 64 MiB limit")
    return data


def _read_record_file(path: Path) -> object:
    try:
        info = path.lstat()
    except OSError as exc:
        raise StoreError("record file is unavailable") from exc
    if path.is_symlink() or not stat.S_ISREG(info.st_mode):
        raise StoreError("record file must be a regular nonsymlink file")
    if info.st_size > MAX_RECORD_BYTES:
        raise StoreError("record file exceeds the 1 MiB limit")
    try:
        with path.open("rb") as stream:
            data = stream.read(MAX_RECORD_BYTES + 1)
        if len(data) > MAX_RECORD_BYTES:
            raise StoreError("record file exceeds the 1 MiB limit")
        return json.loads(data.decode("utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise StoreError("record file is invalid") from exc


@contextlib.contextmanager
def _root_lock(root: Path):
    lock_path = root / ".browser-capture-store.lock"
    try:
        if lock_path.is_symlink():
            raise StoreError("store lock is unavailable")
        handle = lock_path.open("a+b")
        handle.seek(0, os.SEEK_END)
        if handle.tell() == 0:
            handle.write(b"\0")
            handle.flush()
        handle.seek(0)
        if os.name == "nt":
            import msvcrt

            msvcrt.locking(handle.fileno(), msvcrt.LK_LOCK, 1)
        else:
            import fcntl

            fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
    except OSError as exc:
        try:
            handle.close()
        except (UnboundLocalError, OSError):
            pass
        raise StoreError("store lock is unavailable") from exc
    try:
        yield
    finally:
        try:
            if os.name == "nt":
                import msvcrt

                handle.seek(0)
                msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                import fcntl

                fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
        finally:
            handle.close()


def _atomic_publish(path: Path, data: bytes) -> bool:
    """Publish immutable bytes; return False when identical bytes already exist."""
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.parent.is_symlink():
        raise StoreError("destination directory is unavailable")
    try:
        current = path.lstat()
    except FileNotFoundError:
        current = None
    except OSError as exc:
        raise StoreError("destination is unavailable") from exc
    if current is not None:
        if path.is_symlink() or not path.is_file():
            raise StoreError("destination is not a regular file")
        try:
            existing = path.read_bytes()
        except OSError as exc:
            raise StoreError("destination could not be read") from exc
        if existing != data:
            raise StoreError("destination conflicts with existing data")
        return False

    fd = -1
    temp_name = None
    try:
        fd, temp_name = tempfile.mkstemp(prefix=".capture-", dir=path.parent)
        with os.fdopen(fd, "wb") as stream:
            fd = -1
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        try:
            os.link(temp_name, path)
            return True
        except FileExistsError:
            if path.is_symlink() or not path.is_file() or path.read_bytes() != data:
                raise StoreError("destination conflicts with existing data")
            return False
        except OSError as exc:
            raise StoreError("destination could not be published") from exc
    finally:
        if fd >= 0:
            os.close(fd)
        if temp_name:
            try:
                os.unlink(temp_name)
            except FileNotFoundError:
                pass


def _json_bytes(value: dict) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")


def _paths(root: Path, record: dict, suffix: str) -> tuple[Path, Path, Path]:
    image_rel = Path("images") / f"{record['attachment_id']}{suffix}"
    stem = f"{record['record_id']}_{record['attachment_id']}"
    return root / image_rel, root / "metadata" / f"{stem}.txt", root / "metadata" / f"{stem}.json"


def _validate_committed(root: Path, metadata_path: Path) -> tuple[dict | None, str | None]:
    try:
        if metadata_path.is_symlink() or not metadata_path.is_file() or metadata_path.stat().st_size > MAX_RECORD_BYTES:
            return None, "invalid_sidecar"
        raw = json.loads(metadata_path.read_bytes().decode("utf-8"))
        if not isinstance(raw, dict) or raw.get("schema_version") != SCHEMA_VERSION:
            return None, "invalid_sidecar"
        allowed = set(RECORD_FIELDS) | {"schema_version", "local_relative_path", "sha256", "file_size_bytes", "acquisition_timestamp_utc", "source_original_verified"}
        if set(raw) != allowed:
            return None, "invalid_sidecar_fields"
        record = _validate_record({field: raw[field] for field in RECORD_FIELDS})
        expected_sidecar = f"{record['record_id']}_{record['attachment_id']}.json"
        if metadata_path.name != expected_sidecar:
            return None, "sidecar_identity_mismatch"
        if raw.get("source_original_verified") is not False:
            return None, "invalid_verification_claim"
        rel = raw.get("local_relative_path")
        if not isinstance(rel, str):
            return None, "invalid_local_path"
        local = Path(rel)
        if local.is_absolute() or ".." in local.parts or local.parts[:1] != ("images",):
            return None, "invalid_local_path"
        suffix = local.suffix
        if suffix and not EXT_RE.fullmatch(suffix):
            return None, "invalid_local_path"
        if local.parent != Path("images") or local.stem != record["attachment_id"]:
            return None, "local_path_identity_mismatch"
        artifact = root / local
        try:
            info = artifact.lstat()
        except FileNotFoundError:
            return None, "artifact_missing_or_changed"
        if artifact.is_symlink() or not artifact.is_file() or info.st_size != raw.get("file_size_bytes"):
            return None, "artifact_missing_or_changed"
        if info.st_size > MAX_SOURCE_BYTES:
            return None, "artifact_missing_or_changed"
        content = artifact.read_bytes()
        if _hash_bytes(content) != raw.get("sha256"):
            return None, "artifact_hash_mismatch"
        text_path = metadata_path.with_suffix(".txt")
        if text_path.is_symlink() or not text_path.is_file() or text_path.stat().st_size > MAX_RECORD_BYTES or text_path.read_bytes().decode("utf-8") != record["message_text_exact"]:
            return None, "caption_missing_or_changed"
        return {**record, **{key: raw[key] for key in ("local_relative_path", "sha256", "file_size_bytes")}}, None
    except (OSError, ValueError, KeyError, TypeError, UnicodeError):
        return None, "invalid_sidecar"


def add_capture(root: Path, source: Path, raw_record: object) -> dict:
    record = _validate_record(raw_record)
    data = _read_source(source)
    digest = _hash_bytes(data)
    suffix = source.suffix.lower()
    if not EXT_RE.fullmatch(suffix):
        suffix = ""
    try:
        root.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise StoreError("store root is unavailable") from exc
    with _root_lock(root):
        _ensure_store_dirs(root)
        image_path, text_path, json_path = _paths(root, record, suffix)
        if json_path.exists() or json_path.is_symlink():
            committed, reason = _validate_committed(root, json_path)
            if reason:
                raise StoreError("existing capture commit marker is invalid")
            comparison = {key: committed[key] for key in RECORD_FIELDS}
            if comparison != record or committed["sha256"] != digest or committed["file_size_bytes"] != len(data):
                raise StoreError("capture conflicts with existing committed data")
            summary = _rebuild_store_locked(root)
            return {"status": "duplicate", "local_relative_path": committed["local_relative_path"], "sha256": digest, **summary}

        try:
            _atomic_publish(image_path, data)
            _atomic_publish(text_path, record["message_text_exact"].encode("utf-8"))
            metadata = {
                **record,
                "schema_version": SCHEMA_VERSION,
                "local_relative_path": image_path.relative_to(root).as_posix(),
                "sha256": digest,
                "file_size_bytes": len(data),
                "acquisition_timestamp_utc": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                "source_original_verified": False,
            }
            metadata_bytes = _json_bytes(metadata)
            if len(metadata_bytes) > MAX_RECORD_BYTES:
                raise StoreError("record exceeds the 1 MiB limit")
            _atomic_publish(json_path, metadata_bytes)
        except StoreError:
            raise
        except OSError as exc:
            raise StoreError("capture could not be stored") from exc
        summary = _rebuild_store_locked(root)
        return {"status": "added", "local_relative_path": metadata["local_relative_path"], "sha256": digest, **summary}


def rebuild_store(root: Path) -> dict:
    root.mkdir(parents=True, exist_ok=True)
    with _root_lock(root):
        return _rebuild_store_locked(root)


def _rebuild_store_locked(root: Path) -> dict:
    _ensure_store_dirs(root)
    metadata_dir = root / "metadata"
    records = []
    audit_errors = []
    if metadata_dir.exists():
        for sidecar in sorted(metadata_dir.glob("*.json")):
            record, error = _validate_committed(root, sidecar)
            if error:
                audit_errors.append({"sidecar": sidecar.name, "error": error})
            else:
                records.append(record)
    snapshot = {"schema_version": SCHEMA_VERSION, "generated_at_utc": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"), "record_count": len(records), "invalid_sidecars": len(audit_errors), "coverage_complete": False}

    # Write complete replacement snapshots; the sidecars remain authoritative.
    manifest_path = root / "manifest.csv"
    temp_fd, temp_name = tempfile.mkstemp(prefix=".manifest-", dir=root)
    try:
        with os.fdopen(temp_fd, "w", encoding="utf-8", newline="") as stream:
            writer = csv.DictWriter(stream, fieldnames=MANIFEST_FIELDS, extrasaction="ignore")
            writer.writeheader()
            for record in records:
                writer.writerow({key: _csv_safe(record.get(key, "")) for key in MANIFEST_FIELDS})
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp_name, manifest_path)
    finally:
        try:
            os.unlink(temp_name)
        except FileNotFoundError:
            pass

    audit = _json_bytes({"schema_version": SCHEMA_VERSION, "valid_records": len(records), "invalid_sidecars": len(audit_errors), "errors": audit_errors})
    _atomic_replace_bytes(root / "quality_audit.json", audit)
    checkpoint = {
        **snapshot,
        "completed_attachment_ids": sorted({record["attachment_id"] for record in records}),
        "unique_attachment_count": len({record["attachment_id"] for record in records}),
        "completed_records": [{"record_id": record["record_id"], "attachment_id": record["attachment_id"]} for record in records],
        "manifest_sha256": _hash_bytes(manifest_path.read_bytes()),
        "audit_sha256": _hash_bytes(audit),
    }
    _atomic_replace_bytes(root / "checkpoint.json", _json_bytes(checkpoint))
    return snapshot


def _csv_safe(value: object) -> object:
    if isinstance(value, str) and value.startswith(("=", "+", "-", "@", "\t")):
        return "'" + value
    return value


def _ensure_store_dirs(root: Path) -> None:
    resolved_root = root.resolve()
    for name in ("images", "metadata"):
        directory = root / name
        if directory.is_symlink():
            raise StoreError("store directory is unavailable")
        directory.mkdir(parents=True, exist_ok=True)
        if directory.resolve().parent != resolved_root:
            raise StoreError("store directory is unavailable")


def _atomic_replace_bytes(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=".snapshot-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp_name, path)
    except OSError as exc:
        raise StoreError("snapshot could not be written") from exc
    finally:
        try:
            os.unlink(temp_name)
        except FileNotFoundError:
            pass


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    add = sub.add_parser("add", help="store one already-downloaded browser attachment")
    add.add_argument("--root", required=True, type=Path)
    add.add_argument("--source", required=True, type=Path)
    record_source = add.add_mutually_exclusive_group(required=True)
    record_source.add_argument("--record", help="JSON object (metadata may be visible in process listings)")
    record_source.add_argument("--record-file", type=Path, help="read whitelisted JSON metadata from a bounded local file")
    rebuild = sub.add_parser("rebuild", help="rebuild manifest and quality snapshots from committed sidecars")
    rebuild.add_argument("--root", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        if args.command == "add":
            raw_record = _read_record_file(args.record_file) if args.record_file else json.loads(args.record)
            result = add_capture(args.root, args.source, raw_record)
        else:
            result = rebuild_store(args.root)
        print(json.dumps(result, ensure_ascii=False, sort_keys=True))
        return 1 if result.get("invalid_sidecars", 0) else 0
    except (StoreError, json.JSONDecodeError) as exc:
        print(f"capture store error: {exc}", file=sys.stderr)
        return 2
    except OSError:
        print("capture store error: local store is unavailable", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
