export declare class HooversionError extends Error {
    readonly code: number;
    constructor(message: string, code?: number);
}
export declare function assertHooversion(condition: unknown, message: string): asserts condition;
