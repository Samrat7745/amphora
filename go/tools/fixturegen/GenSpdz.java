import io.carbynestack.mpspdz.integration.MpSpdzIntegrationUtils;
import java.math.BigInteger;
import java.nio.file.*;
import java.util.*;

/** Golden vectors for the Go math/big reimplementation of mp-spdz-integration. */
public class GenSpdz {
  static String hex(byte[] b) { StringBuilder s = new StringBuilder(); for (byte x : b) s.append(String.format("%02x", x)); return s.toString(); }
  public static void main(String[] a) throws Exception {
    BigInteger p = new BigInteger("198766463529478683931867765928436695041");
    BigInteger r = new BigInteger("141515903391459779531506841503331516415");
    BigInteger ri = new BigInteger("133854242216446749056083838363708373830");
    MpSpdzIntegrationUtils u = MpSpdzIntegrationUtils.of(p, r, ri);
    StringBuilder out = new StringBuilder();
    out.append("params ").append(p).append(' ').append(r).append(' ').append(ri).append('\n');
    Random rnd = new Random(42);
    List<BigInteger> vs = new ArrayList<>(Arrays.asList(BigInteger.ZERO, BigInteger.ONE, BigInteger.TWO, p.subtract(BigInteger.ONE), p, p.add(BigInteger.ONE), BigInteger.valueOf(-1), p.shiftRight(1), r, ri));
    for (int i = 0; i < 300; i++) vs.add(new BigInteger(127 + rnd.nextInt(2), rnd).mod(p));
    for (BigInteger v : vs) {
      try { String h = hex(u.toGfp(v)); out.append("to ").append(v).append(' ').append(h).append('\n'); }
      catch (Exception e) { out.append("to ").append(v).append(" ERR ").append(e.getClass().getSimpleName()).append(": ").append(e.getMessage()).append('\n'); }
    }
    List<byte[]> bs = new ArrayList<>();
    bs.add(new byte[16]);
    byte[] ff = new byte[16]; Arrays.fill(ff, (byte) 0xff); bs.add(ff);
    for (int i = 0; i < 300; i++) { byte[] b = new byte[16]; rnd.nextBytes(b); bs.add(b); }
    bs.add(new byte[15]); bs.add(new byte[17]);
    for (byte[] b : bs) {
      try { BigInteger h = u.fromGfp(b); out.append("from ").append(hex(b)).append(' ').append(h).append('\n'); }
      catch (Exception e) { out.append("from ").append(hex(b)).append(" ERR ").append(e.getClass().getSimpleName()).append(": ").append(e.getMessage()).append('\n'); }
    }
    String[] names = {"", "a", "123e4567-e89b-12d3-a456-426614174000_odo-computation", "123e4567-e89b-12d3-a456-426614174000_4", "00000000-0000-0000-0000-000000000000_2", "ffffffff-ffff-ffff-ffff-ffffffffffff_1000000"};
    for (String n : names) out.append("uuid ").append(hex(n.getBytes())).append(' ').append(UUID.nameUUIDFromBytes(n.getBytes())).append('\n');
    for (int i = 0; i < 50; i++) { UUID id = new UUID(rnd.nextLong(), rnd.nextLong()); String n = id + "_" + (1 + rnd.nextInt(200)); out.append("uuid ").append(hex(n.getBytes())).append(' ').append(UUID.nameUUIDFromBytes(n.getBytes())).append('\n'); }
    Files.write(Paths.get(a[0]), out.toString().getBytes());
  }
}
