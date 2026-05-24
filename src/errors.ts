export class HooversionError extends Error {
  constructor(message: string, readonly code = 1) {
    super(message);
    this.name = "HooversionError";
  }
}

export function assertHooversion(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new HooversionError(message);
  }
}
