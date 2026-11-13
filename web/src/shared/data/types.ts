export type Interval = { mean: number; lo: number; hi: number };

export type Effect = {
  design: string;
  exposure_bp: number;
  gmv_lift: Interval;
  str_change_pp: Record<string, Interval>;
  cross_border_share_of_gmv: Interval;
};

export type Estimate = { value: number; lo: number; hi: number; truth: number };

export type Design = { id: string; label: string; bias: number; rmse: number; coverage: number; width: number; estimates: Estimate[] };

export type World = {
  world: string;
  seeds: number;
  markets: string[];
  buyers: number;
  listingsPerDay: number;
  testDays: number;
  truth: Effect;
  ramp: Effect[];
  capped: Effect;
  designs: Design[];
  guardrail: { max_domestic_str_drop_pp: number; recommended_exposure_bp: number; gmv_lift_at_recommendation: number };
  recommendation: { exposureBP: number; gmvLift: number };
};

export type Bench = Record<string, unknown>;

export type LabData = { dev: World; heldout?: World; bench?: Bench };
