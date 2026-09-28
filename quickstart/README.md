# Voxie quickstart

Talk to a voice agent from your browser, in any language. One Node 20+
process, no dependencies:

- **:8400:** the call page, and a WHIP proxy to the Voxie server.
- **:9101:** a demo agent answering Voxie's four request types (`listen`,
  `greeting`, `chat`, `oneshot`). This is the part you replace with your app.

```bash
# with the Voxie server (:8080) and voice router (:8300) running
node quickstart/server.mjs
# open http://localhost:8400, click Start call, and speak any language
```

The demo agent replies in your language with Groq (`GROQ_API_KEY`), and
echoes you if the key is unset. The contract it implements is in
[docs/voxie/agent-contract.md](../docs/voxie/agent-contract.md).

| Variable | Default | |
|---|---|---|
| `VOXIE_URL` | `http://127.0.0.1:8080` | The Voxie server |
| `PAGE_PORT` | `8400` | The call page |
| `AGENT_PORT` | `9101` | The demo agent |
| `STREAMCORE_AGENT_API_KEY` | – | Must match `[agent] api_key` on the server |
| `GROQ_API_KEY` | – | Replies with Groq; echoes without it |
| `QUICKSTART_MODEL` | `openai/gpt-oss-20b` | The Groq model |
| `VOICE_HEALTH_URL` | `http://127.0.0.1:8300/health` | Which languages the voice can speak now |
