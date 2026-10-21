export async function readBoundedUTF8Body(body: ReadableStream<Uint8Array> | null, maxBytes: number) {
  if (!body || !Number.isSafeInteger(maxBytes) || maxBytes < 1) throw new Error("invalid body");
  const reader = body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > maxBytes) {
        await reader.cancel("request body exceeds limit");
        throw new Error("request body exceeds limit");
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const document = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    document.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder("utf-8", { fatal: true }).decode(document);
}
