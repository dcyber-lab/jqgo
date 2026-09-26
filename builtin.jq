def values: select(. != null);
def nulls: select(. == null);
def booleans: select(type == "boolean");
def numbers: select(type == "number");
def strings: select(type == "string");
def arrays: select(type == "array");
def objects: select(type == "object");
def iterables: select(type|. == "array" or . == "object");
def scalars: select(type|. != "array" and . != "object");
def finites: select(isinfinite or isnan | not);
def normals: select(isnormal);

def map(f): [.[] | f];
def map_values(f): .[] |= f;
def recurse(f; cond): def r: ., (f | select(cond) | r); r;
def toarray: if type == "array" then . else [.] end;

def add(f): reduce f as $x (null; . + $x);
def del(f): delpaths([path(f)]);
def paths: path(..) | select(length > 0);
def paths(node_filter): path(.. | select(node_filter)) | select(length > 0);
def leaf_paths: paths(scalars);
def pick(pathexps): . as $top | reduce path(pathexps) as $p (null; setpath($p; $top | getpath($p)));
def with_entries(f): to_entries | map(f) | from_entries;

def sort_by(f): _sort_by_impl(map([f]));
def group_by(f): _group_by_impl(map([f]));
def unique_by(f): _unique_by_impl(map([f]));
def min_by(f): _min_by_impl(map([f]));
def max_by(f): _max_by_impl(map([f]));

def in(xs): . as $x | xs | has($x);
def inside(xs): . as $x | xs | contains($x);

def any(generator; condition): isempty(first(generator | condition or empty)) | not;
def all(generator; condition): isempty(first(generator | condition and empty));
def any(condition): any(.[]; condition);
def all(condition): all(.[]; condition);
def any: any(.);
def all: all(.);
def IN(s): any(s == .; .);
def IN(src; s): any(src == s; .);
def INDEX(stream; idx_expr): reduce stream as $row ({}; .[$row | idx_expr | tostring] |= $row);
def INDEX(idx_expr): INDEX(.[]; idx_expr);

def first: .[0];
def last: .[-1];
def nth($n): .[$n];
def last(f): reduce f as $x (null; $x);
def nth($n; f):
  if $n < 0 then error("nth doesn't support negative indices")
  else label $out | foreach f as $item ($n + 1; . - 1; if . <= 0 then $item, break $out else empty end) end;

def combinations: if length == 0 then [] else .[0][] as $x | (.[1:] | combinations) as $w | [$x] + $w end;
def combinations(n): . as $dot | [range(n)] | map($dot) | combinations;
def walk(f): def w: if type == "object" then map_values(w) elif type == "array" then map(w) else . end | f; w;
def transpose: [range(0; map(length) | max // 0) as $i | [.[][$i]]];
def ascii: [.] | implode;
def env: $ENV;
def halt_error: halt_error(5);
def debug(msg): (msg | debug | empty), .;
def error(msg): msg | error;

def todate: strftime("%Y-%m-%dT%H:%M:%SZ");
def todateiso8601: todate;
def fromdateiso8601: strptime("%Y-%m-%dT%H:%M:%SZ") | mktime;
def fromdate: fromdateiso8601;
def date: todate;
def dateadd(u; n): . + n;
def datesub(u; n): . - n;

def scan($re; $flags): match($re; "g" + ($flags // "")) | if (.captures | length) > 0 then [.captures | .[] | .string] else .string end;
def scan($re): scan($re; null);
def splits($re; flags): split($re; flags) | .[];
def splits($re): splits($re; null);
def sub($re; str): sub($re; str; "");
def gsub($re; str; $flags): sub($re; str; $flags + "g");
def gsub($re; str): sub($re; str; "g");

def tostream: path(def r: (.[]? | r), .; r) as $p | getpath($p) | reduce path(.[]?) as $q ([$p, .]; [$p + $q]);
def fromstream(f): {x: null, e: false} as $init
  | foreach f as $i ($init;
      if .e then $init else . end
      | if $i | length == 2
        then setpath(["e"]; $i[0] | length == 0) | setpath(["x"] + $i[0]; $i[1])
        else setpath(["e"]; $i[0] | length == 1) end;
      if .e then .x else empty end);
def truncate_stream(stream): . as $n | null | stream | . as $input
  | if (.[0] | length) > $n then setpath([0]; .[0][$n:]) else empty end;
def JOIN($idx; idx_expr): [.[] | [., $idx[idx_expr]]];
def JOIN($idx; stream; idx_expr): stream | [., $idx[idx_expr]];
def JOIN($idx; stream; idx_expr; join_expr): stream | [., $idx[idx_expr]] | join_expr;
