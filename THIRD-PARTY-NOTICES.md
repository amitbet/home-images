The Unix native JPEG binding uses purego v0.11.2, Copyright 2022 The Ebitengine
Authors, licensed under Apache-2.0:
https://github.com/ebitengine/purego

The Apache license is included in `third-party-licenses/purego-LICENSE`.
Purego also incorporates portions of the Go runtime under the Go project's
BSD-3-Clause license, included in `third-party-licenses/Go-LICENSE`.

The internal HNSW implementation is derived from coder/hnsw v0.6.1,
https://github.com/coder/hnsw, under CC0-1.0. The license is included in
`third-party-licenses/hnsw-CC0`; modifications are described in
`internal/hnsw/README.md` in the source distribution.

Bundled libjpeg-turbo retains its own notices and licenses in
`turbojpeg-licenses/`.

Phone pairing QR codes use go-qrcode, Copyright 2014 Tom Harwood, under the
MIT license included in `third-party-licenses/go-qrcode-LICENSE`.

Enabling phone backup downloads the unmodified Syncthing v2.1.0 executable
from https://github.com/syncthing/syncthing/releases/tag/v2.1.0 . Its license
(MPL-2.0), authors and readme are retained beside the executable under
`data_dir/syncthing/bin/v2.1.0/`. Syncthing runs as a separate process and is
not linked into Home Images. Its source is available at that release tag.

## Local CLIP text search

The packaged `local-text` directory includes ONNX Runtime (Microsoft, MIT) and its
upstream `ThirdPartyNotices.txt`, plus the OpenAI CLIP ViT-B/32 text model and
tokenizer (MIT; `third-party-licenses/CLIP-LICENSE.txt`). Model assets are the Immich
ONNX export at revision `a857c8de2c07bbcfa6646adfcf31b798845afa1e`. ONNX Runtime
release 1.23.2 is distributed with its LICENSE and third-party notices.
