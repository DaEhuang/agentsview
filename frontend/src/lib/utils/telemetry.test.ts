import { afterEach, expect, it, vi } from "vite-plus/test";
import { reportTelemetry } from "./telemetry.js";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("bounds screen requests and preserves earlier event transport", async () => {
  vi.useFakeTimers();
  const fetch = vi.fn((_url: string, options: RequestInit) =>
    options.signal
      ? new Promise<Response>((_resolve, reject) => {
          options.signal!.addEventListener("abort", () => reject(new Error("aborted")));
        })
      : Promise.resolve(new Response("", { status: 202 })),
  );
  vi.stubGlobal("fetch", fetch);
  for (const event of ["app_opened", "screen_viewed"] as const) {
    expect(() => reportTelemetry(event)).not.toThrow();
    const options = fetch.mock.calls.at(-1)![1];
    if (event === "app_opened") {
      expect(options).not.toHaveProperty("signal");
      expect(vi.getTimerCount()).toBe(0);
    } else {
      await vi.advanceTimersByTimeAsync(5000);
      expect(options.signal!.aborted).toBe(true);
      expect(vi.getTimerCount()).toBe(0);
    }
  }
  expect(fetch).toHaveBeenCalledTimes(2);
});
