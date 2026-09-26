# Real fixture corpus

Fixtures captured from real hardware during the capture run live here, one
directory per scenario and NPD variant, each holding `fixture.json` and
`label.json` (see [docs/fixtures.md](../../docs/fixtures.md)).

**Nothing synthetic may be committed here.** Loop devices and fabricated SMART
output are fine for developing collectors and rules; they must never enter
this directory. The published accuracy numbers are the project's
differentiator, and a synthetic fixture would void them.
`tropis eval --publishable` refuses any corpus containing a fixture marked
synthetic.

This directory is empty until the capture run on the rig.
