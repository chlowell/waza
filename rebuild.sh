#! /bin/sh

docker build -t waza .
container=$(docker create waza)
docker cp $container:/usr/local/bin/waza /home/chlowe/code/waza
docker rm -f $container
