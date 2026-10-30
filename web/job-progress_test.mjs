import test from "node:test";
import assert from "node:assert/strict";
import { describeJob } from "./dist/job-progress.js";

test("a transfer with a known size reads as a fraction of it", () => {
  const job = describeJob({ operation: "backup", phase: "uploading", bytes_done: 50 << 20, bytes_total: 200 << 20 });
  assert.equal(job.label, "Uploading to cloud storage");
  assert.equal(job.percent, 25);
  assert.equal(job.fraction, 0.25);
  assert.equal(job.amount, "50 MB / 200 MB");
});

test("a capture reads as bytes written, because its total is not known yet", () => {
  const job = describeJob({ operation: "backup", phase: "capturing", bytes_done: 12 << 20, bytes_total: 0 });
  assert.equal(job.label, "Capturing filesystem");
  assert.equal(job.percent, null, "no total means no percentage to claim");
  assert.equal(job.fraction, 0);
  assert.equal(job.amount, "12 MB");
});

test("every phase of a restore is named", () => {
  assert.equal(describeJob({ phase: "downloading" }).label, "Downloading from cloud storage");
  assert.equal(describeJob({ phase: "importing" }).label, "Importing into a new sandbox");
  assert.equal(describeJob({ phase: "verifying" }).label, "Verifying stored archive");
});

test("nonsense from the server never produces a broken bar", () => {
  // More done than promised would otherwise overflow the meter.
  assert.equal(describeJob({ phase: "uploading", bytes_done: 500, bytes_total: 100 }).fraction, 1);
  assert.equal(describeJob({ phase: "uploading", bytes_done: 500, bytes_total: 100 }).percent, 100);
  assert.equal(describeJob({ phase: "uploading", bytes_done: -5, bytes_total: -5 }).percent, null);
  assert.equal(describeJob({ phase: "uploading", bytes_done: -5, bytes_total: -5 }).amount, "0 B");
  // An unknown phase is shown as itself rather than as blank.
  assert.equal(describeJob({ phase: "rewinding" }).label, "rewinding");
  assert.equal(describeJob({}).label, "Working");
  assert.equal(describeJob(undefined).label, "Working");
});
