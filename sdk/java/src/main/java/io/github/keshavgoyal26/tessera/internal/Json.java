package io.github.keshavgoyal26.tessera.internal;

import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Tiny JSON writer and reader used for JWT claims and Twirp error bodies.
 *
 * <p>This class is internal to the SDK and is not part of its public API. It exists so that the
 * SDK does not need a JSON library at runtime.
 *
 * <p>Supported values: {@link Map} (with {@link String} keys), {@link Collection}, {@link String},
 * {@link Boolean}, {@link Number} and {@code null}. The reader returns {@link LinkedHashMap},
 * {@link ArrayList}, {@link String}, {@link Boolean}, {@link Long} (integral numbers that fit),
 * {@link Double} and {@code null}.
 */
public final class Json {

  private Json() {}

  /**
   * Serializes a value to compact JSON.
   *
   * @param value the value to write
   * @return the JSON text
   * @throws IllegalArgumentException if the value (or a nested value) has an unsupported type
   */
  public static String write(Object value) {
    StringBuilder sb = new StringBuilder();
    writeValue(sb, value);
    return sb.toString();
  }

  private static void writeValue(StringBuilder sb, Object value) {
    if (value == null) {
      sb.append("null");
    } else if (value instanceof String s) {
      writeString(sb, s);
    } else if (value instanceof Boolean b) {
      sb.append(b.booleanValue() ? "true" : "false");
    } else if (value instanceof Double || value instanceof Float) {
      double d = ((Number) value).doubleValue();
      if (Double.isNaN(d) || Double.isInfinite(d)) {
        throw new IllegalArgumentException("JSON cannot represent " + d);
      }
      sb.append(value);
    } else if (value instanceof Number n) {
      sb.append(n);
    } else if (value instanceof Map<?, ?> map) {
      sb.append('{');
      boolean first = true;
      for (Map.Entry<?, ?> e : map.entrySet()) {
        if (!(e.getKey() instanceof String key)) {
          throw new IllegalArgumentException("JSON object keys must be strings");
        }
        if (!first) {
          sb.append(',');
        }
        first = false;
        writeString(sb, key);
        sb.append(':');
        writeValue(sb, e.getValue());
      }
      sb.append('}');
    } else if (value instanceof Collection<?> list) {
      sb.append('[');
      boolean first = true;
      for (Object item : list) {
        if (!first) {
          sb.append(',');
        }
        first = false;
        writeValue(sb, item);
      }
      sb.append(']');
    } else {
      throw new IllegalArgumentException("unsupported JSON value type: " + value.getClass());
    }
  }

  private static void writeString(StringBuilder sb, String s) {
    sb.append('"');
    for (int i = 0; i < s.length(); i++) {
      char c = s.charAt(i);
      switch (c) {
        case '"' -> sb.append("\\\"");
        case '\\' -> sb.append("\\\\");
        case '\b' -> sb.append("\\b");
        case '\f' -> sb.append("\\f");
        case '\n' -> sb.append("\\n");
        case '\r' -> sb.append("\\r");
        case '\t' -> sb.append("\\t");
        default -> {
          // Control characters, plus U+2028/U+2029 which break some JavaScript parsers.
          if (c < 0x20 || c == ' ' || c == ' ') {
            sb.append(String.format("\\u%04x", (int) c));
          } else {
            sb.append(c);
          }
        }
      }
    }
    sb.append('"');
  }

  /**
   * Parses JSON text.
   *
   * @param text the JSON text
   * @return the parsed value
   * @throws IllegalArgumentException if the text is not valid JSON
   */
  public static Object parse(String text) {
    Reader r = new Reader(text);
    r.skipWs();
    Object v = r.readValue(0);
    r.skipWs();
    if (r.pos != text.length()) {
      throw r.error("trailing characters");
    }
    return v;
  }

  private static final class Reader {
    private static final int MAX_DEPTH = 64;
    private final String s;
    private int pos;

    Reader(String s) {
      this.s = s;
    }

    IllegalArgumentException error(String msg) {
      return new IllegalArgumentException("invalid JSON at offset " + pos + ": " + msg);
    }

    void skipWs() {
      while (pos < s.length()) {
        char c = s.charAt(pos);
        if (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
          pos++;
        } else {
          break;
        }
      }
    }

    Object readValue(int depth) {
      if (depth > MAX_DEPTH) {
        throw error("nesting too deep");
      }
      if (pos >= s.length()) {
        throw error("unexpected end of input");
      }
      char c = s.charAt(pos);
      switch (c) {
        case '{':
          return readObject(depth);
        case '[':
          return readArray(depth);
        case '"':
          return readString();
        case 't':
          expect("true");
          return Boolean.TRUE;
        case 'f':
          expect("false");
          return Boolean.FALSE;
        case 'n':
          expect("null");
          return null;
        default:
          if (c == '-' || (c >= '0' && c <= '9')) {
            return readNumber();
          }
          throw error("unexpected character '" + c + "'");
      }
    }

    void expect(String word) {
      if (!s.startsWith(word, pos)) {
        throw error("expected " + word);
      }
      pos += word.length();
    }

    Map<String, Object> readObject(int depth) {
      Map<String, Object> map = new LinkedHashMap<>();
      pos++; // {
      skipWs();
      if (pos < s.length() && s.charAt(pos) == '}') {
        pos++;
        return map;
      }
      while (true) {
        skipWs();
        if (pos >= s.length() || s.charAt(pos) != '"') {
          throw error("expected object key");
        }
        String key = readString();
        skipWs();
        if (pos >= s.length() || s.charAt(pos) != ':') {
          throw error("expected ':'");
        }
        pos++;
        skipWs();
        map.put(key, readValue(depth + 1));
        skipWs();
        if (pos >= s.length()) {
          throw error("unterminated object");
        }
        char c = s.charAt(pos++);
        if (c == '}') {
          return map;
        }
        if (c != ',') {
          throw error("expected ',' or '}'");
        }
      }
    }

    List<Object> readArray(int depth) {
      List<Object> list = new ArrayList<>();
      pos++; // [
      skipWs();
      if (pos < s.length() && s.charAt(pos) == ']') {
        pos++;
        return list;
      }
      while (true) {
        skipWs();
        list.add(readValue(depth + 1));
        skipWs();
        if (pos >= s.length()) {
          throw error("unterminated array");
        }
        char c = s.charAt(pos++);
        if (c == ']') {
          return list;
        }
        if (c != ',') {
          throw error("expected ',' or ']'");
        }
      }
    }

    String readString() {
      pos++; // opening quote
      StringBuilder sb = new StringBuilder();
      while (true) {
        if (pos >= s.length()) {
          throw error("unterminated string");
        }
        char c = s.charAt(pos++);
        if (c == '"') {
          return sb.toString();
        }
        if (c < 0x20) {
          throw error("control character in string");
        }
        if (c != '\\') {
          sb.append(c);
          continue;
        }
        if (pos >= s.length()) {
          throw error("unterminated escape");
        }
        char e = s.charAt(pos++);
        switch (e) {
          case '"' -> sb.append('"');
          case '\\' -> sb.append('\\');
          case '/' -> sb.append('/');
          case 'b' -> sb.append('\b');
          case 'f' -> sb.append('\f');
          case 'n' -> sb.append('\n');
          case 'r' -> sb.append('\r');
          case 't' -> sb.append('\t');
          case 'u' -> {
            if (pos + 4 > s.length()) {
              throw error("truncated \\u escape");
            }
            try {
              sb.append((char) Integer.parseInt(s.substring(pos, pos + 4), 16));
            } catch (NumberFormatException ex) {
              throw error("invalid \\u escape");
            }
            pos += 4;
          }
          default -> throw error("invalid escape '\\" + e + "'");
        }
      }
    }

    Object readNumber() {
      int start = pos;
      if (s.charAt(pos) == '-') {
        pos++;
      }
      boolean integral = true;
      while (pos < s.length()) {
        char c = s.charAt(pos);
        if (c >= '0' && c <= '9') {
          pos++;
        } else if (c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-') {
          integral = false;
          pos++;
        } else {
          break;
        }
      }
      String num = s.substring(start, pos);
      try {
        if (integral) {
          try {
            return Long.parseLong(num);
          } catch (NumberFormatException overflow) {
            return Double.parseDouble(num);
          }
        }
        return Double.parseDouble(num);
      } catch (NumberFormatException ex) {
        throw error("invalid number '" + num + "'");
      }
    }
  }
}
