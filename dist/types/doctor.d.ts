import type { NormalizedConfig } from "./types";
export interface DoctorResult {
    errors: string[];
    warnings: string[];
    info: string[];
}
export declare function runDoctor(cwd: string, config: NormalizedConfig): DoctorResult;
