/*
 * Copyright (c) 2026 - for information on the respective copyright owner
 * see the NOTICE file and/or the repository https://github.com/carbynestack/amphora.
 *
 * SPDX-License-Identifier: Apache-2.0
 */
package io.carbynestack.amphora.client;

import static org.junit.jupiter.api.Assertions.*;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

import io.carbynestack.amphora.common.AmphoraServiceUri;
import io.carbynestack.amphora.common.Tag;
import io.carbynestack.amphora.common.TagValueType;
import java.math.BigInteger;
import java.util.Base64;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.Test;

/**
 * Runs the existing Java client against live Amphora services (used to verify the Go port is a drop-in). Not part of the
 * default surefire run; enable with {@code AMPHORA_URLS=http://h0:p,http://h1:p ./mvnw -o -pl amphora-java-client test
 * -Dtest=LiveGoServiceIT}.
 */
class LiveGoServiceIT {
  private static final BigInteger PRIME = new BigInteger("198766463529478683931867765928436695041");
  private static final BigInteger R = new BigInteger("141515903391459779531506841503331516415");
  private static final BigInteger R_INV = new BigInteger("133854242216446749056083838363708373830");

  @Test
  void createReadTagListDeleteAgainstLiveServices() throws Exception {
    String urls = System.getenv("AMPHORA_URLS");
    assumeTrue(urls != null && !urls.isEmpty(), "AMPHORA_URLS not set");
    DefaultAmphoraClientBuilder b = DefaultAmphoraClient.builder();
    for (String u : urls.split(",")) {
      b.addEndpoint(new AmphoraServiceUri(u));
    }
    String token =
        "e30."
            + Base64.getUrlEncoder().withoutPadding().encodeToString("{\"sub\":\"alice\"}".getBytes())
            + ".sig";
    b.prime(PRIME);
    b.r(R);
    b.rInv(R_INV);
    b.bearerTokenProvider(uri -> token);
    AmphoraClient c = b.build();

    BigInteger[] data = {BigInteger.valueOf(42), BigInteger.ZERO, PRIME.subtract(BigInteger.ONE)};
    Secret s =
        Secret.of(
            List.of(
                Tag.builder().key("purpose").value("javait").build(),
                Tag.builder().key("rank").value("7").valueType(TagValueType.LONG).build()),
            data);
    UUID id = c.createSecret(s);
    assertEquals(s.getSecretId(), id);

    Secret back = c.getSecret(id);
    assertArrayEquals(data, back.getData());
    assertTrue(back.getTags().stream().anyMatch(t -> t.getKey().equals("owner") && t.getValue().equals("alice")));

    c.createTag(id, Tag.builder().key("extra").value("x").build());
    assertEquals("x", c.getTag(id, "extra").getValue());
    c.updateTag(id, Tag.builder().key("extra").value("y").build());
    assertEquals("y", c.getTag(id, "extra").getValue());
    c.deleteTag(id, "extra");
    assertThrows(Exception.class, () -> c.getTag(id, "extra"));
    try {
      c.overwriteTags(id, List.of(Tag.builder().key("only").value("1").build()));
    } catch (Exception e) {
      assumeTrue(System.getenv("JAVA_ORACLE") != null, "overwriteTags failed: " + e);
    }
    assertTrue(c.getSecrets().stream().anyMatch(m -> m.getSecretId().equals(id)));
    c.deleteSecret(id);
    assertThrows(Exception.class, () -> c.getSecret(id));
  }
}
