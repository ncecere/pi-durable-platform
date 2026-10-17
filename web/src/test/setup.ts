import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import * as matchers from "vitest-axe/matchers";
import { afterEach, expect } from "vitest";
import { scaled } from "../../test-scale";

expect.extend(matchers);
// findBy*/waitFor retry for up to 5 s instead of 1 s: on a loaded CI runner a
// page can take longer than 1 s to render after its queries resolve. It only
// waits when the element isn't there yet, so fast runs are no slower.
configure({ asyncUtilTimeout: scaled(5000) }); // times TEST_TIME_SCALE, 1 unless set (test-scale.ts)
afterEach(() => cleanup());
