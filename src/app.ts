#!/usr/bin/env bun
import { HooversionError } from "./errors";
import { loadVersionhooAppConfigFromEnv, startVersionhooApp } from "./app-server";

process.on("uncaughtException", exit);
process.on("unhandledRejection", exit);

try {
  startVersionhooApp(loadVersionhooAppConfigFromEnv());
} catch (error) {
  exit(error);
}

function exit(error: unknown): never {
  if (error instanceof HooversionError) {
    console.error(error.message);
    process.exit(error.code);
  }
  console.error(error);
  process.exit(1);
}
