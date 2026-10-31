// How a running backup or restore is described on a snapshot card.

import { formatBytes } from "./file-utils.js";

const PHASE_LABELS = {
  capturing: "Capturing filesystem",
  uploading: "Uploading to cloud storage",
  verifying: "Verifying stored archive",
  downloading: "Downloading from cloud storage",
  importing: "Importing into a new sandbox",
};

// A promotion runs a restore's two phases but builds an image, not a sandbox.
const PROMOTE_LABELS = { importing: "Importing as a base image" };

// describeJob turns one progress payload into what the card shows. percent is
// null while the total is unknown, which is every capture: an archive's size
// is only settled once it has been written.
export function describeJob(progress) {
  const total = Math.max(0, Number(progress?.bytes_total) || 0);
  const done = Math.max(0, Number(progress?.bytes_done) || 0);
  const known = total > 0;
  // A server that reports more than it promised must not overflow the bar.
  const fraction = known ? Math.min(1, done / total) : 0;
  return {
    label: (progress?.operation === "promote" ? PROMOTE_LABELS[progress?.phase] : null)
      || PHASE_LABELS[progress?.phase] || progress?.phase || "Working",
    amount: known ? `${formatBytes(done)} / ${formatBytes(total)}` : formatBytes(done),
    percent: known ? Math.round(fraction * 100) : null,
    fraction,
  };
}
