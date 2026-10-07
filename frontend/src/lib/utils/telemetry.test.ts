import { afterEach, expect, it, vi } from "vite-plus/test";
import { reportTelemetry } from "./telemetry.js";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("bounds a telemetry request on WebKit without AbortSignal.timeout", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("AbortSignal", { timeout: undefined });
  const fetch = vi.fn(
    (_url: string, options: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        options.signal!.addEventListener("abort", () => reject(new Error("aborted")));
      }),
  );
  vi.stubGlobal("fetch", fetch);
  expect(() =>
    reportTelemetry("screen_viewed", { screen: "sessions", surface: "web" }),
  ).not.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(5000);
  expect(fetch.mock.calls[0]![1].signal!.aborted).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
});
