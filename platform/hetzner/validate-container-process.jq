length == 1
and (.[0] |
  type == "object"
  and (keys | sort == ["args", "env", "gid", "uid", "working_directory"])
  and (.args |
    type == "array"
    and length > 0
    and length <= 1024
    and all(.[];
      type == "string"
      and utf8bytelength <= 65536
      and (explode | index(0) | not)))
  and (.args[0] | length > 0)
  and (.env |
    type == "array"
    and length <= 4096
    and all(.[];
      type == "string"
      and utf8bytelength <= 65536
      and (explode | index(0) | not)))
  and (all(.env[]; test("^[^=]+=")))
  and (([.env[] | split("=")[0]] | length)
    == ([.env[] | split("=")[0]] | unique | length))
  and (.working_directory |
    type == "string"
    and utf8bytelength <= 65536
    and (explode | index(0) | not)
    and startswith("/")
    and (contains("//") | not)
    and (. == "/" or (endswith("/") | not))
    and (test("/(\\.|\\.\\.)($|/)") | not))
  and (.uid |
    type == "number"
    and floor == .
    and . >= 0
    and . <= 4294967295)
  and (.gid |
    type == "number"
    and floor == .
    and . >= 0
    and . <= 4294967295)
)
