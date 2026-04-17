import mimetypes
import os
import secrets
import tempfile
from dataclasses import dataclass
from fastapi import FastAPI, File, Form, Header, HTTPException, UploadFile
from faster_whisper import WhisperModel

MODEL_SIZE = os.environ.get("WHISPER_MODEL", "base")
DEVICE = os.environ.get("WHISPER_DEVICE", "cpu")
COMPUTE = os.environ.get("WHISPER_COMPUTE", "int8")
# Optional bearer; unset disables the check. Pair with a 127.0.0.1
# bind (Dockerfile default) or a trusted compose-internal network.
AUTH_TOKEN = os.environ.get("WHISPER_AUTH_TOKEN", "")

app = FastAPI()
_model = WhisperModel(MODEL_SIZE, device=DEVICE, compute_type=COMPUTE)


@dataclass
class TranscribeResult:
    text: str
    language: str


def _safe_suffix(file: UploadFile) -> str:
    # Prefer a suffix derived from the declared content-type; fall back
    # to the sanitised filename extension. Never trust the raw filename
    # (null bytes crash NamedTemporaryFile; path separators write to
    # unexpected directories).
    if file.content_type:
        exts = mimetypes.guess_all_extensions(file.content_type)
        if exts:
            return exts[0]
    name = file.filename or ""
    _, ext = os.path.splitext(os.path.basename(name))
    ext = ext.replace("\x00", "")
    if any(c in ext for c in ("/", "\\")):
        return ""
    return ext


@app.post("/inference", response_model=None)
async def inference(
    file: UploadFile = File(...),
    language: str = Form(default=None),
    authorization: str = Header(default=""),
) -> TranscribeResult:
    if AUTH_TOKEN:
        expected = f"Bearer {AUTH_TOKEN}"
        if not secrets.compare_digest(authorization, expected):
            raise HTTPException(status_code=401, detail="unauthorized")

    suffix = _safe_suffix(file)
    with tempfile.NamedTemporaryFile(suffix=suffix, delete=False) as tmp:
        tmp.write(await file.read())
        tmp_path = tmp.name

    try:
        kw = {"language": language} if language else {}
        segments, info = _model.transcribe(tmp_path, **kw)
        text = " ".join(s.text.strip() for s in segments)
    finally:
        os.unlink(tmp_path)

    return TranscribeResult(text=text, language=info.language)
