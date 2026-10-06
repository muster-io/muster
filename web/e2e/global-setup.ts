// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Starts `muster dev` for the Playwright specs, as the Go end-to-end harness does: a fresh database on the development
// PostgreSQL (or the server of MUSTER_E2E_DATABASE_URL), the binary of MUSTER_E2E_BINARY (default ../bin/muster) with
// its fake servers, and a wait until it is ready. The returned teardown stops it and drops the database.

import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { resolve } from "node:path";

import { Client } from "pg";

const DEFAULT_SERVER = "postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable";
const READY_URL = "http://localhost:8082/health/ready";
// The last address line of `muster dev`, printed once its fake servers listen.
const FAKES_READY = "muster dev: fake Telegram ";
const READY_TIMEOUT_MS = 60_000;

async function admin(server: string, statement: string): Promise<void> {
  const url = new URL(server);
  url.pathname = "/postgres";
  const client = new Client({ connectionString: url.toString() });
  await client.connect();
  try {
    await client.query(statement);
  } finally {
    await client.end();
  }
}

async function ready(): Promise<boolean> {
  try {
    return (await fetch(READY_URL)).ok;
  } catch {
    return false;
  }
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  if (await ready()) {
    throw new Error(
      "a Muster already listens on :8082; stop it before running the end-to-end specs",
    );
  }
  const server = process.env.MUSTER_E2E_DATABASE_URL ?? DEFAULT_SERVER;
  const name = `muster_e2e_web_${randomBytes(4).toString("hex")}`;
  await admin(server, `CREATE DATABASE ${name}`);
  const database = new URL(server);
  database.pathname = `/${name}`;
  process.env.MUSTER_E2E_WEB_DATABASE_URL = database.toString();

  const binary = resolve(process.env.MUSTER_E2E_BINARY ?? "../bin/muster");
  const child = spawn(binary, ["dev"], {
    env: { ...process.env, MUSTER_DATABASE_URL: database.toString() },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  const collect = (chunk: Buffer) => {
    // The start of the output is enough to see readiness or a failure; the rest is not kept.
    output = (output + chunk.toString()).slice(0, 100_000);
  };
  child.stdout.on("data", collect);
  child.stderr.on("data", collect);
  const exited = new Promise<void>((done) => child.once("exit", () => done()));

  const teardown = async () => {
    if (child.exitCode === null) {
      child.kill("SIGTERM");
      await Promise.race([exited, new Promise((done) => setTimeout(done, 15_000))]);
      if (child.exitCode === null) {
        child.kill("SIGKILL");
      }
    }
    await admin(server, `DROP DATABASE IF EXISTS ${name} WITH (FORCE)`);
  };

  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (!(output.includes(FAKES_READY) && (await ready()))) {
    if (child.exitCode !== null || Date.now() > deadline) {
      await teardown();
      throw new Error(`muster dev did not become ready:\n${output}`);
    }
    await new Promise((done) => setTimeout(done, 200));
  }
  return teardown;
}
