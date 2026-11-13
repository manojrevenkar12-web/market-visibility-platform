import { useQuery } from "@tanstack/react-query";
import type { LabData } from "./types";

export async function fetchLabData(): Promise<LabData> {
  const res = await fetch("./data.json");
  if (!res.ok) throw new Error(`The report could not be loaded (HTTP ${res.status}).`);
  return (await res.json()) as LabData;
}

export function useLabData() {
  return useQuery({ queryKey: ["lab-data"], queryFn: fetchLabData, retry: false, staleTime: Infinity });
}

export function hasResults(d: LabData | undefined): d is LabData {
  return Boolean(d?.dev && d.dev.designs.length > 0 && d.dev.ramp.length > 0);
}
