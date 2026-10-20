/**
 * The version of the runtime contract (docs/runtime-contract.md) this library implements: the HTTP API, `/status`,
 * standby and activation, the reports to the platform, ownership and the environment. It goes up by one when a
 * runtime built on an older contract would no longer work with the platform, or the other way round; additions that
 * both sides may ignore keep it.
 */
export const CONTRACT_VERSION = 1;

/**
 * The pi-durable version the library is built and tested on (pinned in package.json; a test checks that both and the
 * installed package agree). Reported at `/status` so the platform can tell which storage format an agent writes.
 */
export const PI_DURABLE_VERSION = "1.3.0";
