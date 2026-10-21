import { createCipheriv, createDecipheriv, randomBytes } from "node:crypto";

const keyBytes = 32;
const nonceBytes = 12;
const tagBytes = 16;

export class ConfigCipher {
  constructor(private readonly key: Buffer) {
    if (key.length !== keyBytes) throw new TypeError("configuration encryption key must be 32 bytes");
    this.key = Buffer.from(key);
  }

  static configured(environment: NodeJS.ProcessEnv = process.env): ConfigCipher {
    const encoded = environment.CANTELOP_CONFIG_ENCRYPTION_KEY?.trim();
    if (!encoded) throw new Error("CANTELOP_CONFIG_ENCRYPTION_KEY is required");
    const key = Buffer.from(encoded, "base64url");
    if (key.toString("base64url") !== encoded) throw new TypeError("invalid configuration encryption key");
    return new ConfigCipher(key);
  }

  seal(appID: string, name: string, value: string): { nonce: Buffer; ciphertext: Buffer } {
    const nonce = randomBytes(nonceBytes);
    const cipher = createCipheriv("aes-256-gcm", this.key, nonce);
    cipher.setAAD(aad(appID, name));
    const encrypted = Buffer.concat([cipher.update(value, "utf8"), cipher.final()]);
    return { nonce, ciphertext: Buffer.concat([encrypted, cipher.getAuthTag()]) };
  }

  open(appID: string, name: string, nonce: Buffer, ciphertext: Buffer): string {
    if (nonce.length !== nonceBytes || ciphertext.length < tagBytes) throw new Error("invalid encrypted configuration value");
    const body = ciphertext.subarray(0, -tagBytes);
    const decipher = createDecipheriv("aes-256-gcm", this.key, nonce);
    decipher.setAAD(aad(appID, name));
    decipher.setAuthTag(ciphertext.subarray(-tagBytes));
    return Buffer.concat([decipher.update(body), decipher.final()]).toString("utf8");
  }
}

function aad(appID: string, name: string): Buffer {
  return Buffer.from(`cantelop-app-config-v1\0${appID}\0${name}`, "utf8");
}
