import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "./api/client";
import { appRetryDelay, shouldRetry } from "./lib/retry";
import { router } from "./router";
// bitop-ui (installed with the bitop CLI, see README.md): font, tokens, the
// neutral theme and base styles. The server sets <html data-brand> from
// UI_THEME when it serves index.html ("neutral" is the only theme).
import "@/components/ui/styles/bitop.css";

const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    // A 401 anywhere means the session ended: show the sign-in page.
    onError: (err, query) => {
      if (err instanceof ApiError && err.status === 401 && query.queryKey[0] !== "me") {
        queryClient.setQueryData(["me"], null);
      }
    },
  }),
  mutationCache: new MutationCache({
    onError: (err) => {
      // A save conflict (412): load the latest version of whatever is on screen, so the form can show what
      // changed and save over it on purpose. Forms keep the person's edits (useRevisionForm).
      if (err instanceof ApiError && err.status === 412) void queryClient.invalidateQueries();
    },
  }),
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      // Client errors (4xx) will not fix themselves: retry server and network errors, and a rate limit after its Retry-After.
      retry: shouldRetry,
      // Also tells the shell's rate-limit banner when the next try is.
      retryDelay: appRetryDelay,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
