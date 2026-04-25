export function log(
  level: string,
  msg: string,
  attrs?: Record<string, unknown>,
): void {
  process.stderr.write(
    JSON.stringify({ time: new Date().toISOString(), level, msg, ...attrs }) +
      '\n',
  );
}
