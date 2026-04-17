FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY main.py .
# Bind to loopback by default; flip to 0.0.0.0 only when deploying
# behind a compose-internal network AND setting WHISPER_AUTH_TOKEN.
CMD ["uvicorn", "main:app", "--host", "127.0.0.1", "--port", "8178"]
