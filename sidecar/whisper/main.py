import os
import tempfile
from fastapi import FastAPI, UploadFile, File, Form
from faster_whisper import WhisperModel

MODEL_SIZE = os.environ.get("WHISPER_MODEL", "base")
DEVICE = os.environ.get("WHISPER_DEVICE", "cpu")
COMPUTE = os.environ.get("WHISPER_COMPUTE", "int8")

app = FastAPI()
_model = WhisperModel(MODEL_SIZE, device=DEVICE, compute_type=COMPUTE)


@app.post("/inference")
async def inference(file: UploadFile = File(...), model: str = Form(default=None)):
    with tempfile.NamedTemporaryFile(suffix=file.filename, delete=False) as tmp:
        tmp.write(await file.read())
        tmp_path = tmp.name

    try:
        segments, _ = _model.transcribe(tmp_path)
        text = " ".join(s.text.strip() for s in segments)
    finally:
        os.unlink(tmp_path)

    return {"text": text}
