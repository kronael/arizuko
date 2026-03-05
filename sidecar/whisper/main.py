import os
import tempfile
from dataclasses import dataclass
from fastapi import FastAPI, UploadFile, File, Form
from faster_whisper import WhisperModel

MODEL_SIZE = os.environ.get("WHISPER_MODEL", "base")
DEVICE = os.environ.get("WHISPER_DEVICE", "cpu")
COMPUTE = os.environ.get("WHISPER_COMPUTE", "int8")

app = FastAPI()
_model = WhisperModel(MODEL_SIZE, device=DEVICE, compute_type=COMPUTE)


@dataclass
class TranscribeResult:
    text: str
    language: str


@app.post("/inference", response_model=None)
async def inference(
    file: UploadFile = File(...),
    language: str = Form(default=None),
) -> TranscribeResult:
    with tempfile.NamedTemporaryFile(suffix=file.filename, delete=False) as tmp:
        tmp.write(await file.read())
        tmp_path = tmp.name

    try:
        kw = {"language": language} if language else {}
        segments, info = _model.transcribe(tmp_path, **kw)
        text = " ".join(s.text.strip() for s in segments)
    finally:
        os.unlink(tmp_path)

    return TranscribeResult(text=text, language=info.language)
