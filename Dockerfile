# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/muster /usr/local/bin/muster

# 65532 is distroless's nonroot user, numeric so that the kubelet can check runAsNonRoot without runAsUser.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/muster"]
