# Changelog

## [0.2.0](https://github.com/00Webbo/tropis/compare/v0.1.1...v0.2.0) (2026-10-07)


### ⚠ BREAKING CHANGES

* **eval:** the gate now applies to end-to-end detection instead of root-cause accuracy, and runner.GateMinRootCauseAccuracy is replaced by runner.GateMinEndToEndDetection.
* **reason:** PromptVersion is v2; eval results are not comparable with v1 results.

### Features

* **eval:** gate on end-to-end detection ([f023f1f](https://github.com/00Webbo/tropis/commit/f023f1f7a64f49a40abd7a5771ac263cef533a48))
* **prefilter:** raise nodes on Kubernetes storage symptoms ([0bdf6c2](https://github.com/00Webbo/tropis/commit/0bdf6c2de1d262246f6df12b3bf18a53a10e4fdf))


### Bug fixes

* **reason:** keep trigger names out of model input ([3cfd7ca](https://github.com/00Webbo/tropis/commit/3cfd7ca1524979eddc7c5cc9df0f854fbd551144))

## [0.1.1](https://github.com/00Webbo/tropis/compare/v0.1.0...v0.1.1) (2026-10-05)


### Bug fixes

* **local:** stop Ollama silently truncating the model input ([bf241bb](https://github.com/00Webbo/tropis/commit/bf241bb2b442b256f118e3d500fb06ed3195d6dc))

## 0.1.0 (2026-10-05)


### Features

* Tropis v1 diagnostic agent, evaluation harness and packaging ([79c6397](https://github.com/00Webbo/tropis/commit/79c6397b8ea51bfecb0b84721cd22fba4fdc1918))
