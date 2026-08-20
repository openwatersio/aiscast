declare module "ggencoder" {
  class AisEncode {
    constructor(msg: Record<string, unknown>);
    valid: boolean;
    nmea: string;
  }
  const ggencoder: { AisEncode: typeof AisEncode };
  export default ggencoder;
}
