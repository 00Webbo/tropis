# Model profiles

Tropis needs a reasoning backend. Which one fits depends on your hardware
and on whether cluster data may leave the cluster. Each profile below is a
values file in
[deploy/helm/tropis/examples/](../deploy/helm/tropis/examples/) that sets
only what differs from the chart's defaults. Install one with `-f`:

```sh
helm install tropis oci://ghcr.io/i-am-webbo/charts/tropis -n tropis-system \
  --create-namespace -f local-gpu-8gb.yaml
```

Change `baseURL`, or the Secret name for the claude profiles, to match your
cluster. Every setting is described with `backend` in
[values.yaml](../deploy/helm/tropis/values.yaml).

| Profile | Hardware | What leaves the cluster | Time per analysed node | Dev-corpus behaviour (9-case subset) | Caveats |
|---|---|---|---|---|---|
| [claude](../deploy/helm/tropis/examples/claude.yaml) | None; an Anthropic API key in a Secret | The redacted evidence of each node the pre-filter raises, sent to Anthropic ([SECURITY.md](../SECURITY.md#what-leaves-the-cluster)) | Not yet measured | Not yet measured | The chart's default |
| [claude-sweep-escalate](../deploy/helm/tropis/examples/claude-sweep-escalate.yaml) | As claude | As claude, sent once or twice per node | Not yet measured | Not yet measured | Cheaper and less accurate: a sweep that is confidently wrong is never escalated |
| [local-gpu-8gb](../deploy/helm/tropis/examples/local-gpu-8gb.yaml) | A GPU with about 8 GB, running Ollama with `qwen3.5:9b` (6.6 GB) | Nothing | 1–3 min (RTX 3070) | 3/5 detected end to end; 0/2 false causal claims; 0 errors; `insufficient_evidence` correct 1/2 | Called a read-only remount and a Kubernetes-layer fault coincidental |
| [local-cpu](../deploy/helm/tropis/examples/local-cpu.yaml) | CPU only, running Ollama with `qwen3.5:4b` (3.3 GB, ~4 GB RAM loaded) | Nothing | 3–13 min (16-thread CPU) | 2/5 detected end to end; 0/2 false causal claims; 1 error; `insufficient_evidence` correct 2/2 | Slow, and misses more faults than local-gpu-8gb. One case in nine ran away in hidden reasoning and failed as truncated |

"Detected end to end" means the pre-filter raised the fault and the verdict
named the right relationship and layer. "False causal claims" counts the
negative controls (the two coincidental cases) called causal. The terms are
defined in [eval/README.md](../eval/README.md#what-is-measured).

## What the numbers are, and are not

**The dev corpus is synthetic.** These numbers come from fixtures generated
from `eval/scenarios`, not captured from real hardware. They compare the
profiles with each other on the same cases and must not be quoted as
Tropis's accuracy. The published number will come from the capture run on
real hardware ([docs/capture-rig.md](capture-rig.md)). Until then there is
none.

Nine cases are too few for small differences to mean much. Read the table
as "roughly works" or "does not", not as a ranking.

The subset is nine `npd-absent` fixtures from
[eval/testdata](../eval/testdata/):

| Case | Truth | local-gpu-8gb | local-cpu |
|---|---|---|---|
| degradation-fsync | causal, host | causal ✓ | causal ✓ |
| read-errors-postgres | causal, host | causal ✓ | error: truncated ✗ |
| pending-sectors-slow-probes | causal, host | causal ✓ | causal ✓ |
| write-errors-readonly-remount | causal, host | coincidental ✗ | insufficient_evidence ✗ |
| exhaustion-log-spam | causal, kubernetes | coincidental ✗ | coincidental ✗ |
| stable-defects-oom | coincidental | coincidental ✓ | coincidental ✓ |
| read-errors-unused-region-config-crash | coincidental | coincidental ✓ | coincidental ✓ |
| pending-no-symptoms | insufficient_evidence | insufficient_evidence ✓ | insufficient_evidence ✓ |
| unreadable-smart-restarts | insufficient_evidence | coincidental ✗ | insufficient_evidence ✓ |
| **Whole subset** | | **18m40s** | **52m41s** |

Measured in October 2026 at prompt v3, with `--concurrency 1 --seed 1`, on
an RTX 3070 (8 GB), 31 GB of RAM and a 16-thread CPU. The GPU run used
Ollama 0.40.3 and the CPU runs 0.40.6. Thinking was left at each model's
default. The CPU runs used a second Ollama server with the GPU hidden
(`CUDA_VISIBLE_DEVICES=-1`), so nothing was offloaded to it.

Also tried for local-cpu, and not recommended:

- **`qwen3.5:2b`** finished faster (25m38s) but gave a verdict on only
  three of the nine cases. Four of its outputs cited evidence that is not
  in the input, two were truncated, and it never answered
  `insufficient_evidence`. 1/5 detected end to end.
- **`qwen3.5:4b` with `contextTokens: 32768`** gave the same verdicts on
  the cases it finished. On the case that truncated at 16384, it reasoned
  for 27,000 tokens and hit the 40-minute timeout instead, and the subset
  took 1h19m instead of 52m41s. A larger window did not stop the runaway;
  it only delayed the error.

To reproduce, copy those fixture directories into a scratch directory and
run, for example:

```sh
tropis eval --corpus <dir> --backend local --base-url http://localhost:11434 \
  --model qwen3.5:9b --concurrency 1 --seed 1
```

A CPU model needs a per-case `--timeout` longer than the default five
minutes.

## Choosing

- **Data may leave and accuracy matters most:** claude.
- **Data may leave, and the cost of calling the default model for every
  candidate node matters more than some accuracy:** claude-sweep-escalate.
  Compare it with claude on your own nodes before relying on it.
- **Nothing may leave and you have a GPU of about 8 GB:** local-gpu-8gb.
  A larger GPU can run a larger model: set `backend.model`, and check that
  `backend.contextTokens` still fits.
- **Nothing may leave and there is no GPU:** local-cpu, knowing that it is
  slow and wrong more often. Only nodes the pre-filter raises are
  analysed, and one after another, so on a healthy cluster it is rarely
  called, but a sweep that raises several nodes takes several times as
  long. The profile runs sweeps hourly with a two-hour timeout.

Do not set `backend.localThink: "false"` to speed up a local model. On the
dev corpus that made the model much faster but overconfident, and it made
false causal claims it did not make with thinking on.
