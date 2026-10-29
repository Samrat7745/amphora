import io.carbynestack.amphora.common.FactorPair;
import io.carbynestack.amphora.common.MultiplicationExchangeObject;
import io.carbynestack.castor.common.entities.*;
import org.apache.commons.lang3.SerializationUtils;
import java.io.*;
import java.math.BigInteger;
import java.nio.file.*;
import java.util.*;

/** Golden fixtures (Java serialization of the Redis values) for the Go port. */
public class Gen {
  static Path out;
  static void w(String name, byte[] b) throws Exception { Files.write(out.resolve(name), b); }
  public static void main(String[] a) throws Exception {
    out = Paths.get(a[0]);
    Files.createDirectories(out);
    byte[] raw = new byte[32 * 3];
    for (int i = 0; i < raw.length; i++) raw[i] = (byte) (i * 5 + 1);
    TupleList<InputMask<Field.Gfp>, Field.Gfp> tl = TupleList.fromStream(InputMask.class, Field.GFP, new ByteArrayInputStream(raw), raw.length);
    w("inputmask_list3.bin", SerializationUtils.serialize(tl));
    w("inputmask_list3.raw", raw);
    w("inputmask_list0.bin", SerializationUtils.serialize(TupleList.fromStream(InputMask.class, Field.GFP, new ByteArrayInputStream(new byte[0]), 0)));
    byte[] raw1 = new byte[32];
    TupleList one = TupleList.fromStream(InputMask.class, Field.GFP, new ByteArrayInputStream(raw1), 32);
    w("inputmask_list1_zero.bin", SerializationUtils.serialize(one));
    UUID id = UUID.fromString("123e4567-e89b-12d3-a456-426614174000");
    BigInteger big = new BigInteger("198766463529478683931867765928436695040");
    List<FactorPair> l = new ArrayList<>();
    l.add(FactorPair.of(BigInteger.ZERO, BigInteger.ONE));
    l.add(FactorPair.of(big, big.negate()));
    l.add(FactorPair.of(BigInteger.valueOf(-1), BigInteger.valueOf(255)));
    l.add(FactorPair.of(BigInteger.valueOf(256), BigInteger.valueOf(-256)));
    l.add(FactorPair.of(new BigInteger("340282366920938463463374607431768211456"), new BigInteger("-170141183460469231731687303715884105728")));
    w("meo.bin", SerializationUtils.serialize(new MultiplicationExchangeObject(id, 2, l)));
    w("meo_empty.bin", SerializationUtils.serialize(new MultiplicationExchangeObject(id, 0, new ArrayList<>())));
    // JSON as Jackson writes it for the inter-vcp wire
    w("meo.json", new com.fasterxml.jackson.databind.ObjectMapper().writeValueAsBytes(new MultiplicationExchangeObject(id, 2, l)));
    System.out.println("ok");
  }
}
