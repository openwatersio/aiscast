import { createContext } from "react-router";
import type { ApiAuth } from "./api";

/** Where server renders fetch from, and the token that gives them their own rate limit. */
export const serverEnv = createContext<ApiAuth>();

/** Roughly where the visitor is, as [longitude, latitude], from their network address. */
export const visitorLocation = createContext<[number, number] | undefined>();
