#!/usr/bin/env node
/**
 * scuttlectl npm wrapper.
 * Downloads the appropriate scuttlectl binary for the current platform
 * and executes it, passing all arguments through.
 */
"use strict";

const { execFileSync } = require("child_process");
const { createWriteStream, existsSync, mkdirSync, chmodSync } = require("fs");
const { get } = require("https");
const { join } = require("path");
const { createGunzip } = require("zlib");
const { Extract } = require("tar");

const REPO = "ConflictHQ/scuttlebot";
const BIN_DIR = join(__dirname, "..", "dist");
const BIN_PATH = join(BIN_DIR, process.platform === "win32" ? "scuttlectl.exe" : "scuttlectl");

function platformSuffix() {
  const os = process.platform === "darwin" ? "darwin" : "linux";
  const arch = process.arch === "arm64" ? "arm64" : "x86_64";
  return `${os}-${arch}`;
}

async function fetchLatestVersion() {
  return new Promise((resolve, reject) => {
    get(
      `https://api.github.com/repos/${REPO}/releases/latest`,
      { headers: { "User-Agent": "scuttlectl-npm" } },
      (res) => {
        let data = "";
        res.on("data", (c) => (data += c));
        res.on("end", () => {
          try {
            resolve(JSON.parse(data).tag_name);
          } catch (e) {
            reject(e);
          }
        });
      }
    ).on("error", reject);
  });
}

async function download(url, dest) {
  return new Promise((resolve, reject) => {
    get(url, { headers: { "User-Agent": "scuttlectl-npm" } }, (res) => {
      if (res.statusCode === 302 || res.statusCode === 301) {
        return download(res.headers.location, dest).then(resolve).catch(reject);
      }
      mkdirSync(BIN_DIR, { recursive: true });
      const out = createWriteStream(dest);
      res.pipe(out);
      out.on("finish", resolve);
      out.on("error", reject);
    }).on("error", reject);
  });
}

async function ensureBinary() {
  if (existsSync(BIN_PATH)) return;

  const version = await fetchLatestVersion();
  const suffix = platformSuffix();
  const asset = `scuttlectl-${version}-${suffix}.tar.gz`;
  const url = `https://github.com/${REPO}/releases/download/${version}/${asset}`;
  const tarPath = join(BIN_DIR, asset);

  process.stderr.write(`Downloading scuttlectl ${version}...\n`);
  await download(url, tarPath);

  await new Promise((resolve, reject) => {
    require("fs")
      .createReadStream(tarPath)
      .pipe(createGunzip())
      .pipe(Extract({ cwd: BIN_DIR, strip: 0 }))
      .on("finish", resolve)
      .on("error", reject);
  });

  chmodSync(BIN_PATH, 0o755);
  require("fs").unlinkSync(tarPath);
}

ensureBinary()
  .then(() => {
    execFileSync(BIN_PATH, process.argv.slice(2), { stdio: "inherit" });
  })
  .catch((err) => {
    process.stderr.write(`scuttlectl: ${err.message}\n`);
    process.exit(1);
  });
