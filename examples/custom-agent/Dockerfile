# The agent's image, on the platform's runtime image: the runtime image is the
# library (/app is the package @pdp/runtime, with its dependencies installed),
# and the agent's sources go next to it in /app/agent, where they import the
# library by its name. Nothing is installed here, so the agent uses only the
# library (its facade re-exports what an author needs from pi-durable); an
# agent with npm dependencies of its own needs an install stage.
#
# RUNTIME_IMAGE must be the runtime of the platform version the agent was
# built and tested with: in this repository, the one `make docker-runtime`
# builds from runtime/ (VERSION=dev below); a released one is
# ghcr.io/ncecere/pi-durable-platform-runtime:<version>, pinned by digest
# (@sha256:...) for anything that runs.
#
#   make docker-runtime VERSION=dev
#   docker build -t custom-agent:dev examples/custom-agent
#
# The platform runs it like the runtime image (AGENTS_IMAGE), with the same
# environment, user, read-only root filesystem and exit codes.
ARG RUNTIME_IMAGE=pi-durable-platform-runtime:dev
FROM ${RUNTIME_IMAGE}
COPY src /app/agent
CMD ["node", "--import", "tsx", "agent/main.ts"]
