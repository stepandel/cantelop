import { expect, test } from "bun:test";

import { CloudflareConfigurationProvider } from "./cloudflare-provider";

test("replaces the complete sorted Worker binding set", async () => {
  let captured;
  const provider = new CloudflareConfigurationProvider({
    accountID: "0123456789abcdef0123456789abcdef",
    dispatchNamespace: "cantelop-apps",
    apiToken: "private-token",
    apiBaseURL: "https://cloudflare.example/client/v4",
  }, async (url, init) => {
    captured = { url, init, settings: JSON.parse(await init.body.get("settings").text()) };
    return Response.json({ success: true, result: {} });
  });
  await provider.replaceConfiguration({
    appID: "app_0123456789abcdef0123456789abcdef",
    revision: 2,
    values: [
      { name: "TOKEN", kind: "secret", value: "secret" },
      { name: "LOG_LEVEL", kind: "variable", value: "debug" },
    ],
  });
  expect(captured.url).toContain("/scripts/app_0123456789abcdef0123456789abcdef/settings");
  expect(captured.init.method).toBe("PATCH");
  expect(captured.settings).toEqual({ bindings: [
    { name: "LOG_LEVEL", text: "debug", type: "plain_text" },
    { name: "TOKEN", text: "secret", type: "secret_text" },
  ] });
});

test("user configuration replacement preserves platform-owned bindings",async()=>{
 let settings;
 const provider=new CloudflareConfigurationProvider({accountID:"0".repeat(32),dispatchNamespace:"apps",apiToken:"token"},async(_url,init)=>{settings=JSON.parse(await init.body.get("settings").text());return Response.json({success:true})});
 await provider.replaceConfiguration({appID:"app_"+"0".repeat(32),revision:1,values:[],systemBindings:[{name:"CANTELOP_INTEGRATION_TOKEN",kind:"secret",value:"a".repeat(64)},{name:"CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",kind:"variable",value:"900"}]});
 expect(settings.bindings).toEqual([{name:"CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",type:"plain_text",text:"900"},{name:"CANTELOP_INTEGRATION_TOKEN",type:"secret_text",text:"a".repeat(64)}]);
});

test("a rotation grace period projects the previous token and its expiry",async()=>{
 let settings;
 const provider=new CloudflareConfigurationProvider({accountID:"0".repeat(32),dispatchNamespace:"apps",apiToken:"token"},async(_url,init)=>{settings=JSON.parse(await init.body.get("settings").text());return Response.json({success:true})});
 const base=[{name:"CANTELOP_INTEGRATION_TOKEN",kind:"secret",value:"a".repeat(64)},{name:"CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS",kind:"variable",value:"900"}];
 const previous={name:"CANTELOP_INTEGRATION_TOKEN_PREVIOUS",kind:"secret",value:"b".repeat(64)};
 const expiry={name:"CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT",kind:"variable",value:"2026-10-14T12:00:00.000Z"};
 const project=systemBindings=>provider.replaceConfiguration({appID:"app_"+"0".repeat(32),revision:2,values:[],systemBindings});
 await project([...base,previous,expiry]);
 expect(settings.bindings.map(binding=>[binding.name,binding.type])).toEqual([
  ["CANTELOP_DEFAULT_KEEP_ALIVE_SECONDS","plain_text"],["CANTELOP_INTEGRATION_TOKEN","secret_text"],
  ["CANTELOP_INTEGRATION_TOKEN_PREVIOUS","secret_text"],["CANTELOP_INTEGRATION_TOKEN_PREVIOUS_EXPIRES_AT","plain_text"]]);
 for(const invalid of [
  [...base,previous],
  [...base,expiry,previous],
  [...base,{...previous,value:"a".repeat(64)},expiry],
  [...base,{...previous,kind:"variable"},expiry],
  [...base,previous,{...expiry,value:"tomorrow"}],
  [...base,previous,{...expiry,kind:"secret"}],
 ]) await expect(project(invalid)).rejects.toThrow("invalid system bindings");
});
