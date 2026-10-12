import { afterEach, expect, it, vi } from "vitest";
import { postBoligaIngest, startLookup } from "./api";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(response: unknown) {
  const fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 202,
    json: async () => response,
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

it("carries the ingest token from the lookup to the upload", async () => {
  const fetch = stubFetch({ lookup_id: "abc123", ingest_token: "s3cret" });

  const session = await startLookup("Vestergade 1", 500, 0, null);
  expect(session).toEqual({ lookupId: "abc123", ingestToken: "s3cret" });

  await postBoligaIngest(session, { fetched: [], failed: [] });

  // Without the token the server answers 403 and refetches every street
  // itself, which is the whole of the relay's saving given up.
  const body = JSON.parse(vi.mocked(fetch).mock.calls[1][1].body);
  expect(body).toMatchObject({ lookup_id: "abc123", ingest_token: "s3cret" });
});
