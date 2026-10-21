import "server-only";

import { controlDatabase } from "./db/database";
import { ProductRepository } from "./db/product-repository";
import { ProductService } from "./product-service";

let sharedService: ProductService | undefined;

export function productControl(): ProductService {
  if (!sharedService) sharedService = new ProductService(ProductRepository.fromPool(controlDatabase()));
  return sharedService;
}
