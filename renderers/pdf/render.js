#!/usr/bin/env node
"use strict";

const { chromium } = require("playwright-core");
const path = require("path");

function parseMargins(raw) {
  if (!raw) return { top: "1in", bottom: "1in", left: "1in", right: "1in" };
  const parts = raw.trim().split(/\s+/);
  if (parts.length === 1) {
    return { top: parts[0], bottom: parts[0], left: parts[0], right: parts[0] };
  }
  if (parts.length === 2) {
    return { top: parts[0], bottom: parts[0], left: parts[1], right: parts[1] };
  }
  if (parts.length === 4) {
    return { top: parts[0], right: parts[1], bottom: parts[2], left: parts[3] };
  }
  return { top: raw, bottom: raw, left: raw, right: raw };
}

function parseArgs(args) {
  const opts = {};
  let i = 0;
  while (i < args.length) {
    const arg = args[i];
    if (arg === "--size") {
      opts.size = args[++i];
    } else if (arg === "--margins") {
      opts.margins = args[++i];
    } else if (arg === "--landscape") {
      opts.landscape = true;
    } else if (arg === "--header") {
      opts.header = args[++i];
    } else if (arg === "--footer") {
      opts.footer = args[++i];
    } else if (arg === "--scale") {
      opts.scale = parseFloat(args[++i]);
    } else if (!opts.input) {
      opts.input = arg;
    } else if (!opts.output) {
      opts.output = arg;
    }
    i++;
  }
  return opts;
}

async function render(opts) {
  if (!opts.input || !opts.output) {
    console.error("Usage: render.js <input.html> <output.pdf> [options]");
    process.exit(1);
  }

  const browser = await chromium.launch({
    executablePath: process.env.CHROME_PATH || "/usr/bin/chromium",
    args: ["--no-sandbox", "--disable-setuid-sandbox"],
  });

  try {
    const page = await browser.newPage();
    const inputPath = path.resolve(opts.input);
    await page.goto(`file://${inputPath}`, { waitUntil: "networkidle" });

    await page.pdf({
      path: opts.output,
      format: opts.size || "A4",
      margin: parseMargins(opts.margins),
      landscape: opts.landscape || false,
      printBackground: true,
      scale: opts.scale || 1,
      displayHeaderFooter: !!(opts.header || opts.footer),
      headerTemplate: opts.header || "",
      footerTemplate: opts.footer || "",
    });

    console.log(path.resolve(opts.output));
  } finally {
    await browser.close();
  }
}

const opts = parseArgs(process.argv.slice(2));
render(opts).catch((err) => {
  console.error(err.message);
  process.exit(1);
});
