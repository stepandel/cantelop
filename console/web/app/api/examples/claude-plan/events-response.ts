import type { SessionRef } from '@cantelop/sdk';

export class InvalidCursorError extends Error {}

/** Adapt the SDK subscription to browser SSE; disconnecting never cancels work. */
export function eventsResponse(session: Pick<SessionRef<unknown, unknown>, 'stream'>, request: Request): Response {
  const last = request.headers.get('last-event-id');
  const match = last?.match(/^(.+):(\d+)$/);
  if (last && (!match || !Number.isSafeInteger(Number(match[2])))) throw new InvalidCursorError('Invalid event cursor');
  const controller = new AbortController();
  const abort = () => controller.abort(request.signal.reason);
  request.signal.addEventListener('abort', abort, {once:true});
  if (request.signal.aborted) abort();
  const iterator = session.stream({signal:controller.signal,
    ...(match ? {after:{streamId:match[1]!,sequence:Number(match[2])}} : {})})[Symbol.asyncIterator]();
  // Subscribe before responding so work dispatched after the response cannot outrun the subscription.
  let first: ReturnType<typeof iterator.next> | undefined = iterator.next();
  first.catch(() => undefined);
  const encoder = new TextEncoder();
  const cleanup = () => {controller.abort();request.signal.removeEventListener('abort',abort);};
  return new Response(new ReadableStream<Uint8Array>({
    start(stream) {stream.enqueue(encoder.encode(": connected\n\n"));},
    async pull(stream) {
      try {
        const next = await (first ?? iterator.next());
        first = undefined;
        if (next.done) {cleanup();stream.close();return;}
        const event = next.value;
        stream.enqueue(encoder.encode(`id: ${event.cursor.streamId}:${event.cursor.sequence}\ndata: ${JSON.stringify(event)}\n\n`));
      } catch (error) {cleanup();stream.error(error);}
    },
    async cancel() {cleanup();await iterator.return?.();},
  }), {headers:{'content-type':'text/event-stream','cache-control':'private, no-cache, no-transform','x-accel-buffering':'no'}});
}
