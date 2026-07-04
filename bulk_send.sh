time for i in $(seq 1 5); do
  echo "SET key:$i val:$i"
done | redis-cli --pipe
