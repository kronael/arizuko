export interface FlushResult<T> {
  sent: T[];
  failed: { item: T; err: Error }[];
}

// Drains `queue` in FIFO order, invoking `sendFn` for each item. A throw from
// sendFn is captured per-item; the remaining items still get processed.
export async function flushQueue<T>(
  queue: T[],
  sendFn: (item: T) => Promise<void>,
): Promise<FlushResult<T>> {
  const sent: T[] = [];
  const failed: { item: T; err: Error }[] = [];
  while (queue.length > 0) {
    const item = queue.shift()!;
    try {
      await sendFn(item);
      sent.push(item);
    } catch (e) {
      failed.push({ item, err: e instanceof Error ? e : new Error(String(e)) });
    }
  }
  return { sent, failed };
}
