import { redirect } from "react-router";

// The map's first address. The query goes along, so `?station=` still reaches its station.
function toVessels(request: Request): never {
  throw redirect(`/vessels${new URL(request.url).search}`, 301);
}

export const loader = ({ request }: { request: Request }) => toVessels(request);
export const clientLoader = ({ request }: { request: Request }) => toVessels(request);
