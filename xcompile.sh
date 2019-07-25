#!/bin/sh

APPNAME=`basename ${PWD}`
rm release.txt
for OS in windows linux darwin; do
  mkdir -p ${OS}
  GOOS=${OS} GOARCH=amd64 go build -o ${OS}/${APPNAME}
  if [ ${OS} == "windows" ]; then
    mv windows/${APPNAME} windows/${APPNAME}.exe
  fi
  zip -jr ${APPNAME}-${OS}-amd64.zip ${OS}/${APPNAME}*
  shasum -a 256 ${APPNAME}-${OS}-amd64.zip >>release.txt
done
