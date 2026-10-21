import "server-only";
import { controlDatabase } from "../db/database";
import { FleetRepository } from "./repository";
export function fleetControl() {
  return new FleetRepository(controlDatabase());
}
