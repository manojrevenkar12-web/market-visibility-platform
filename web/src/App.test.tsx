import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import type { Effect, LabData, World } from "./shared/data/types";

const iv = (mean: number) => ({ mean, lo: mean - 0.01, hi: mean + 0.01 });
const effect = (bp: number, lift: number): Effect => ({
  design: `ramp_${bp}`, exposure_bp: bp, gmv_lift: iv(lift), cross_border_share_of_gmv: iv(0.1),
  str_change_pp: { FR: iv(1), DE: iv(-0.2) },
});

function world(truth: number, recommended: number): World {
  const est = (v: number) => Array.from({ length: 4 }, (_, i) => ({ value: v + i * 0.001, lo: v - 0.02, hi: v + 0.02, truth }));
  return {
    world: "dev", seeds: 4, markets: ["FR", "DE"], buyers: 9400, listingsPerDay: 2300, testDays: 28,
    truth: effect(10000, truth), ramp: [effect(1000, truth / 3), effect(10000, truth)], capped: effect(10000, truth * 0.9),
    designs: [
      { id: "listing_split", label: "Listing split", bias: 0.1, rmse: 0.1, coverage: 0, width: 0.04, estimates: est(truth + 0.1) },
      { id: "switchback", label: "Switchback", bias: 0, rmse: 0.01, coverage: 1, width: 0.04, estimates: est(truth) },
    ],
    guardrail: { max_domestic_str_drop_pp: 1, recommended_exposure_bp: 10000, gmv_lift_at_recommendation: truth },
    recommendation: { exposureBP: recommended, gmvLift: truth },
  };
}

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><App /></QueryClientProvider>);
}

const ok = (d: unknown) => () => Promise.resolve(new Response(JSON.stringify(d), { status: 200 }));

function mockFetch(responses: Array<() => Promise<Response>>) {
  let i = 0;
  const fn = vi.fn(() => responses[Math.min(i++, responses.length - 1)]());
  vi.stubGlobal("fetch", fn);
  return fn;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("dashboard", () => {
  it("shows a skeleton while loading", () => {
    mockFetch([() => new Promise(() => {})]);
    renderApp();
    expect(screen.getByRole("status", { name: /loading the report/i })).toBeTruthy();
  });

  it("explains a failed load and retries", async () => {
    const f = mockFetch([() => Promise.resolve(new Response("", { status: 500 })), ok({ dev: world(0.06, 10000) } satisfies LabData)]);
    renderApp();
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: /try again/i }));
    await screen.findByRole("heading", { name: /should FR and DE see each other/i });
    expect(f).toHaveBeenCalledTimes(2);
  });

  it("says what to run when there are no results", async () => {
    mockFetch([ok({ dev: { ...world(0.06, 10000), designs: [] } })]);
    renderApp();
    await screen.findByText(/no results yet/i);
  });

  it("states the truth and what the ramp comparison would have said", async () => {
    mockFetch([ok({ dev: world(0.06, 10000) })]);
    renderApp();
    await screen.findByText(/adds 6\.0% GMV/);
    expect(screen.getByText(/would have reported \+16\.2%/)).toBeTruthy();
    expect(screen.getByRole("group", { name: /Listing split: bias \+10\.0%, interval holds the truth in 0 of 4/ })).toBeTruthy();
  });

  it("switches to the held-out world, where the answer is to stay closed", async () => {
    mockFetch([ok({ dev: world(0.06, 10000), heldout: world(-0.02, 0) })]);
    renderApp();
    fireEvent.click(await screen.findByRole("radio", { name: /held-out/i }));
    await waitFor(() => expect(screen.getByText(/costs 2\.0% GMV/)).toBeTruthy());
    expect((await screen.findAllByText(/stay closed/)).length).toBeGreaterThan(0);
  });
});
