all(.services[];
  (.privileged // false) == false and
  (.network_mode // "") != "host" and
  (.pid // "") != "host" and
  (.ipc // "") != "host" and
  (.read_only // false) == true and
  ((.cap_add // []) | length) == 0 and
  ((.cap_drop // []) | index("ALL")) != null and
  ((.devices // []) | length) == 0 and
  ((.ports // []) | length) == 0 and
  all(.volumes[]?;
    .type == "volume" and
    (.source | contains("docker.sock") | not) and
    (.target | contains("docker.sock") | not)) and
  all((.environment // {}) | to_entries[]?;
    .key == "WIP_SMOKE_ROLE" or .key == "WIP_SMOKE_RUN_ID")) and
all(.networks[]; (.internal // false) == true)
