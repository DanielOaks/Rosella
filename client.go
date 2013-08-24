package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"time"
)

func (c *Client) setNick(nick string) {
	if c.nick != "" {
		delete(c.server.clientMap, c.nick)
		for _, channel := range c.channelMap {
			delete(channel.clientMap, c.nick)
		}
	}

	//Set up new nick
	oldNick := c.nick
	c.nick = nick
	c.server.clientMap[c.nick] = c

	clients := make([]string, 0, 100)

	for _, channel := range c.channelMap {
		channel.clientMap[c.nick] = c

		//Collect list of client nicks who can see us
		for client := range channel.clientMap {
			clients = append(clients, client)
		}
	}

	//By sorting the nicks and skipping duplicates we send each client one message
	sort.Strings(clients)
	prevNick := ""
	for _, nick := range clients {
		if nick == prevNick {
			continue
		}
		prevNick = nick

		client, exists := c.server.clientMap[nick]
		if exists {
			client.reply(rplNickChange, oldNick, c.nick)
		}
	}
}

func (c *Client) joinChannel(channelName string) {
	channel, exists := c.server.channelMap[channelName]
	if exists == false {
		channel = &Channel{name: channelName,
			topic:     "",
			clientMap: make(map[string]*Client)}
		c.server.channelMap[channelName] = channel
	}

	channel.clientMap[c.nick] = c
	c.channelMap[channelName] = channel

	for _, client := range channel.clientMap {
		client.reply(rplJoin, c.nick, channelName)
	}

	if channel.topic != "" {
		c.reply(rplTopic, channelName, channel.topic)
	} else {
		c.reply(rplNoTopic, channelName)
	}

	nicks := make([]string, 0, 100)
	for nick := range channel.clientMap {
		nicks = append(nicks, nick)
	}

	c.reply(rplNames, channelName, strings.Join(nicks, " "))
}

func (c *Client) partChannel(channelName string) {
	channel, exists := c.server.channelMap[channelName]
	if exists == false {
		return
	}

	//Notify clients of the part
	for _, client := range channel.clientMap {
		client.reply(rplPart, c.nick, channelName)
	}

	delete(channel.clientMap, c.nick)
	delete(c.channelMap, channelName)
}

func (c *Client) disconnect() {
	c.connected = false
	c.signalChan <- signalStop
}

//Send a reply to a user with the code specified
func (c *Client) reply(code int, args ...string) {
	if c.connected == false {
		return
	}

	switch code {
	case rplWelcome:
		c.outputChan <- fmt.Sprintf(":%s 001 %s :Welcome to %s", c.server.name, c.nick, c.server.name)
	case rplJoin:
		c.outputChan <- fmt.Sprintf(":%s JOIN %s", args[0], args[1])
	case rplPart:
		c.outputChan <- fmt.Sprintf(":%s PART %s", args[0], args[1])
	case rplTopic:
		c.outputChan <- fmt.Sprintf(":%s 332 %s %s :%s", c.server.name, c.nick, args[0], args[1])
	case rplNoTopic:
		c.outputChan <- fmt.Sprintf(":%s 331 %s %s :No topic is set", c.server.name, c.nick, args[0])
	case rplNames:
		//TODO: break long lists up into multiple messages
		c.outputChan <- fmt.Sprintf(":%s 353 %s = %s :%s", c.server.name, c.nick, args[0], args[1])
		c.outputChan <- fmt.Sprintf(":%s 366 %s", c.server.name, c.nick)
	case rplNickChange:
		c.outputChan <- fmt.Sprintf(":%s NICK %s", args[0], args[1])
	case rplKill:
		c.outputChan <- fmt.Sprintf(":%s KILL %s A %s", c.server.name, c.nick, args[0])
	case rplMsg:
		c.outputChan <- fmt.Sprintf(":%s PRIVMSG %s %s", args[0], args[1], args[2])
	case rplList:
		c.outputChan <- fmt.Sprintf(":%s 321 %s", c.server.name, c.nick)
		for _, listItem := range args {
			c.outputChan <- fmt.Sprintf(":%s 322 %s %s", c.server.name, c.nick, listItem)
		}
		c.outputChan <- fmt.Sprintf(":%s 323 %s", c.server.name, c.nick)
	case rplOper:
		c.outputChan <- fmt.Sprintf(":%s 381 %s :You are now an operator", c.server.name, c.nick)
	case errMoreArgs:
		c.outputChan <- fmt.Sprintf(":%s 461 %s :Not enough params", c.server.name, c.nick)
	case errNoNick:
		c.outputChan <- fmt.Sprintf(":%s 431 %s :No nickname given", c.server.name, c.nick)
	case errInvalidNick:
		c.outputChan <- fmt.Sprintf(":%s 432 %s %s :Erronenous nickname", c.server.name, c.nick, args[0])
	case errNickInUse:
		c.outputChan <- fmt.Sprintf(":%s 433 %s %s :Nick already in use", c.server.name, c.nick, args[0])
	case errAlreadyReg:
		c.outputChan <- fmt.Sprintf(":%s 462 :You need a valid nick first", c.server.name)
	case errNoSuchNick:
		c.outputChan <- fmt.Sprintf(":%s 401 %s %s :No such nick/channel", c.server.name, c.nick, args[0])
	case errUnknownCommand:
		c.outputChan <- fmt.Sprintf(":%s 421 %s %s :Unknown command", c.server.name, c.nick, args[0])
	case errNotReg:
		c.outputChan <- fmt.Sprintf(":%s 451 :You have not registered", c.server.name)
	case errPassword:
		c.outputChan <- fmt.Sprintf(":%s 464 %s :Error, password incorrect", c.server.name, c.nick)
	case errNoPriv:
		c.outputChan <- fmt.Sprintf(":%s 481 %s :Permission denied", c.server.name, c.nick)
	}
}

func (c *Client) clientThread() {
	defer c.connection.Close()

	readSignalChan := make(chan int, 3)
	writeSignalChan := make(chan int, 3)
	writeChan := make(chan string, 100)

	go c.readThread(readSignalChan)
	go c.writeThread(writeSignalChan, writeChan)

	defer func() {
		//Part from all channels
		for channelName := range c.channelMap {
			c.partChannel(channelName)
		}

		delete(c.server.clientMap, c.nick)
	}()

	for {
		select {
		case signal := <-c.signalChan:
			if signal == signalStop {
				readSignalChan <- signalStop
				writeSignalChan <- signalStop
				return
			}
		case line := <-c.outputChan:
			select {
			case writeChan <- line:
				//It worked
			default:
				log.Printf("Dropped a line for client: %q", c.nick)
				//Do nothing, dropping the line
			}
		}
	}

}

func (c *Client) readThread(signalChan chan int) {
	for {
		select {
		case signal := <-signalChan:
			if signal == signalStop {
				return
			}
		default:
			c.connection.SetReadDeadline(time.Now().Add(time.Second * 3))
			buf := make([]byte, 512)
			ln, err := c.connection.Read(buf)
			if err != nil {
				if err == io.EOF {
					c.disconnect()
					return
				}
				continue
			}

			rawLines := buf[:ln]
			lines := bytes.Split(rawLines, []byte("\r\n"))
			for _, line := range lines {
				if len(line) > 0 {
					c.server.eventChan <- Event{client: c, input: string(line)}
				}
			}
		}
	}
}

func (c *Client) writeThread(signalChan chan int, outputChan chan string) {
	for {
		select {
		case signal := <-signalChan:
			if signal == signalStop {
				return
			}
		case output := <-outputChan:
			line := []byte(fmt.Sprintf("%s\r\n", output))

			c.connection.SetWriteDeadline(time.Now().Add(time.Second * 30))
			_, err := c.connection.Write(line)
			if err != nil {
				c.disconnect()
				return
			}
		}
	}
}